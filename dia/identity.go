package dia

import (
	"blossm.local/backend/eventstore"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type SceneResult struct {
	Scene, Group, Principal Ref
	Token                   string
}

func (s *Service) RegisterScene(ctx context.Context, token, key string, subject Ref) (SceneResult, error) {
	if e := validKey(key); e != nil {
		return SceneResult{}, e
	}
	c, e := s.authenticate(ctx, token, "access")
	recovering := e != nil
	if recovering {
		if s.JWT == nil {
			return SceneResult{}, ErrUnavailable
		}
		c, e = s.JWT.Verify(token, "access")
		if e != nil || c.Context.Network != s.Network {
			return SceneResult{}, ErrUnauthorized
		}
	}
	if subject.ID == "" || subject.Network != s.Network || subject.Service == "" || subject.Domain == "" {
		return SceneResult{}, ErrInvalid
	}
	seed := c.Context.Session.Key() + key
	scene := s.root("core", "scene", stableID(seed, "scene"))
	group := s.root("core", "group", stableID(seed, "group"))
	principal := c.Context.Principal
	if principal.ID == "" {
		principal = s.root("core", "principal", stableID(seed, "principal"))
	}
	var prior SceneState
	existing, e := s.load(ctx, scene, &prior)
	if e != nil {
		return SceneResult{}, e
	}
	if existing.Version > 0 {
		if recovering {
			expected := c.Context
			expected.Principal = principal
			if prior.Subject != subject || prior.Group != group {
				return SceneResult{}, eventstore.ErrIdempotency
			}
			freshToken, err := s.recoverAccess(ctx, token, expected)
			if err != nil {
				return SceneResult{}, err
			}
			freshClaims, err := s.authenticate(ctx, freshToken, "access")
			if err != nil {
				return SceneResult{}, err
			}
			if err = s.require(ctx, freshClaims, scene, "core:scene:read"); err != nil {
				return SceneResult{}, err
			}
			return SceneResult{Scene: scene, Group: group, Principal: principal, Token: freshToken}, nil
		}
		if !same(prior.Subject, subject) {
			return SceneResult{}, eventstore.ErrIdempotency
		}
		if e = s.require(ctx, c, scene, "core:scene:read"); e != nil {
			return SceneResult{}, e
		}
		return SceneResult{Scene: scene, Group: group, Principal: principal, Token: token}, nil
	}
	if recovering {
		return SceneResult{}, ErrUnauthorized
	}
	var p PrincipalState
	pa, e := s.load(ctx, principal, &p)
	if e != nil {
		return SceneResult{}, e
	}
	var session SessionState
	sa, e := s.load(ctx, c.Context.Session, &session)
	if e != nil {
		return SceneResult{}, e
	}
	ev := []eventstore.Input{s.event(scene, "register", key+"/scene", 0, SceneState{Subject: subject, Group: group}), s.event(group, "add-principals", key+"/group", 0, map[string]any{"principals": []Ref{principal}}), s.event(group, "add-networks", key+"/networks", 1, map[string]any{"networks": []string{s.Network}}), s.event(principal, "add-roles", key+"/roles", pa.Version, map[string]any{"roles": []RoleGrant{{ID: "SceneAdmin", Subject: scene}, {ID: "GroupAdmin", Subject: group}}})}
	if c.Context.Principal.ID == "" {
		ev = append(ev, s.event(c.Context.Session, "upgrade", key+"/upgrade", sa.Version, map[string]Ref{"principal": principal}))
	}
	if e = s.append(ctx, ev...); e != nil {
		return SceneResult{}, e
	}
	sc := c.Context
	sc.Principal = principal
	t, e := s.sign(ctx, c, "access", sc, c.Expires)
	return SceneResult{Scene: scene, Group: group, Principal: principal, Token: t}, e
}
func (s *Service) DeleteScene(ctx context.Context, token, key string, scene Ref) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	if e = s.require(ctx, c, scene, "core:scene:delete"); e != nil {
		return e
	}
	var st SceneState
	a, e := s.load(ctx, scene, &st)
	if e != nil {
		return e
	}
	if st.Deleted != "" {
		return nil
	}
	return s.append(ctx, s.event(scene, "delete", key, a.Version, map[string]string{"deleted": s.now().Format(time.RFC3339Nano)}))
}

type GroupPrincipal struct {
	Principal Ref      `json:"principal"`
	Roles     []string `json:"roles"`
}

func (s *Service) AddGroupPrincipals(ctx context.Context, token, key string, group Ref, members []GroupPrincipal) (Ref, error) {
	principals := make([]Ref, 0, len(members))
	grants := map[Ref][]string{}
	for _, m := range members {
		if len(m.Roles) == 0 {
			return Ref{}, ErrInvalid
		}
		for _, role := range m.Roles {
			if role != "GroupAdmin" && role != "GroupMember" {
				return Ref{}, ErrInvalid
			}
		}
		principals = append(principals, m.Principal)
		grants[m.Principal] = append(grants[m.Principal], m.Roles...)
	}
	return s.addPrincipals(ctx, token, key, group, principals, grants)
}
func (s *Service) AddPrincipals(ctx context.Context, token, key string, group Ref, principals []Ref) (Ref, error) {
	return s.addPrincipals(ctx, token, key, group, principals, nil)
}
func (s *Service) addPrincipals(ctx context.Context, token, key string, group Ref, principals []Ref, requested map[Ref][]string) (Ref, error) {
	if e := validKey(key); e != nil {
		return Ref{}, e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return Ref{}, e
	}
	if c.Context.Principal.ID == "" {
		return Ref{}, ErrForbidden
	}
	fresh := group.ID == ""
	if fresh {
		group = s.root("core", "group", stableID(c.Context.Principal.Key()+key, "group"))
	} else if e = s.require(ctx, c, group, "core:group:update"); e != nil {
		return Ref{}, e
	}
	var st GroupState
	a, e := s.load(ctx, group, &st)
	if e != nil {
		return Ref{}, e
	}
	if a.Version == 0 && !fresh {
		return Ref{}, eventstore.ErrNotFound
	}
	if fresh && a.Version > 0 {
		if e = s.require(ctx, c, group, "core:group:update"); e != nil {
			return Ref{}, e
		}
	}
	if len(principals) == 0 || len(principals) > 100 {
		return Ref{}, ErrInvalid
	}
	unique := map[Ref]bool{}
	for _, p := range st.Principals {
		unique[p] = true
	}
	for _, p := range principals {
		if p.Domain != "principal" || p.Service != "core" || p.Network != s.Network || p.ID == "" {
			return Ref{}, ErrInvalid
		}
		unique[p] = true
	}
	if fresh {
		unique[c.Context.Principal] = true
	}
	if len(unique) > 100 {
		return Ref{}, errors.New("group exceeds 100 principals")
	}
	if fresh {
		principals = append(principals, c.Context.Principal)
	}
	ev := []eventstore.Input{s.event(group, "add-principals", key+"/members", a.Version, map[string]any{"principals": principals})}
	if fresh {
		ev = append(ev, s.event(group, "add-networks", key+"/network", a.Version+1, map[string]any{"networks": []string{s.Network}}))
	}
	seen := map[Ref]bool{}
	for _, r := range principals {
		if seen[r] {
			continue
		}
		seen[r] = true
		var ps PrincipalState
		pa, e := s.load(ctx, r, &ps)
		if e != nil {
			return Ref{}, e
		}
		ids := requested[r]
		if len(ids) == 0 {
			ids = []string{"GroupMember"}
		}
		roles := []RoleGrant{}
		for _, id := range ids {
			roles = append(roles, RoleGrant{ID: id, Subject: group})
		}
		if fresh && r == c.Context.Principal {
			roles = append(roles, RoleGrant{ID: "GroupAdmin", Subject: group})
		}
		ev = append(ev, s.event(r, "add-roles", key+"/roles/"+r.ID, pa.Version, map[string]any{"roles": roles}))
	}
	if e = s.append(ctx, ev...); e != nil {
		return Ref{}, e
	}
	return group, nil
}
func (s *Service) RemovePrincipals(ctx context.Context, token, key string, group Ref, principals []Ref) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	if e = s.require(ctx, c, group, "core:group:update"); e != nil {
		return e
	}
	if committed, lookupErr := s.committedPayload(ctx, group, "remove-principals", key+"/members", map[string]any{"principals": principals}); lookupErr != nil || committed {
		return lookupErr
	}
	var st GroupState
	a, e := s.load(ctx, group, &st)
	if e != nil {
		return e
	}
	ev := []eventstore.Input{s.event(group, "remove-principals", key+"/members", a.Version, map[string]any{"principals": principals})}
	seen := map[Ref]bool{}
	for _, r := range principals {
		if seen[r] {
			continue
		}
		seen[r] = true
		var p PrincipalState
		pa, e := s.load(ctx, r, &p)
		if e != nil {
			return e
		}
		var roles []RoleGrant
		for _, role := range p.Roles {
			if same(role.Subject, group) {
				roles = append(roles, role)
			}
		}
		if len(roles) > 0 {
			ev = append(ev, s.event(r, "remove-roles", key+"/roles/"+r.ID, pa.Version, map[string]any{"roles": roles}))
		}
	}
	return s.append(ctx, ev...)
}
func (s *Service) GroupPrincipals(ctx context.Context, token string, group Ref) ([]Ref, error) {
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return nil, e
	}
	if e = s.require(ctx, c, group, "core:group:read"); e != nil {
		return nil, e
	}
	var st GroupState
	_, e = s.load(ctx, group, &st)
	return st.Principals, e
}
func (s *Service) PrincipalGroups(ctx context.Context, token string) ([]Ref, error) {
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return nil, e
	}
	all, e := s.groupsForPrincipal(ctx, c.Context.Principal)
	if e != nil {
		return nil, e
	}
	out := []Ref{}
	for _, a := range all {
		var st GroupState
		if e = json.Unmarshal(a.State, &st); e != nil {
			return nil, e
		}
		for _, p := range st.Principals {
			if same(p, c.Context.Principal) {
				out = append(out, a.Root)
				break
			}
		}
	}
	return out, nil
}
func (s *Service) PrincipalRoles(ctx context.Context, token string, subjects []Ref) ([]RoleGrant, error) {
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return nil, e
	}
	var st PrincipalState
	_, e = s.load(ctx, c.Context.Principal, &st)
	if e != nil {
		return nil, e
	}
	out := []RoleGrant{}
	for _, r := range st.Roles {
		if r.Subject.Network != s.Network {
			continue
		}
		if len(subjects) == 0 {
			out = append(out, r)
			continue
		}
		for _, sub := range subjects {
			if same(sub, r.Subject) {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
}
func (s *Service) RolePermissions(ctx context.Context, token, id string) ([]string, error) {
	_, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return nil, e
	}
	if p, ok := BuiltinRoles()[id]; ok {
		return p, nil
	}
	var role RoleState
	a, e := s.load(ctx, s.root("core", "role", id), &role)
	if e != nil {
		return nil, e
	}
	if a.Version == 0 {
		return nil, eventstore.ErrNotFound
	}
	return role.Permissions, nil
}

// ChangeRoles permits delegating only permissions already held over the exact subject.
func (s *Service) ChangeRoles(ctx context.Context, token, key string, principal Ref, roles []RoleGrant, remove bool) error {
	return s.changeRoles(ctx, token, key, principal, roles, remove, nil, Ref{})
}

func (s *Service) changeRoles(ctx context.Context, token, key string, principal Ref, roles []RoleGrant, remove bool, request json.RawMessage, receipt Ref) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	if principal.Network != s.Network || principal.Service != "core" || principal.Domain != "principal" {
		return ErrInvalid
	}
	action := "add-roles"
	if remove {
		action = "remove-roles"
	}
	// Pin each dynamic definition used for authorization to this atomic grant.
	// Permission edits and grants must conflict on the same role version.
	dynamic := make(map[string][]string)
	var touches []eventstore.Input
	builtins := BuiltinRoles()
	for _, g := range roles {
		perms, builtin := builtins[g.ID]
		if !builtin {
			var loaded bool
			perms, loaded = dynamic[g.ID]
			if !loaded {
				root := s.root("core", "role", g.ID)
				var role RoleState
				snapshot, err := s.load(ctx, root, &role)
				if err != nil {
					return err
				}
				if snapshot.Version == 0 {
					return eventstore.ErrNotFound
				}
				perms = role.Permissions
				dynamic[g.ID] = perms
				touches = append(touches, s.event(root, "touch", action+"/"+key+"/"+principal.Key(), snapshot.Version, struct{}{}))
			}
		}
		if len(perms) == 0 {
			return ErrForbidden
		}
		for _, p := range perms {
			if e = s.require(ctx, c, g.Subject, p); e != nil {
				return e
			}
		}
	}
	var state PrincipalState
	a, e := s.load(ctx, principal, &state)
	if e != nil {
		return e
	}
	events := []eventstore.Input{s.event(principal, action, key, a.Version, map[string]any{"roles": roles})}
	return s.appendAtomicRequest(ctx, append(events, touches...), request, receipt)
}

// CreateRole defines a permission set; it confers no authority until an authorized grant.
func (s *Service) CreateRole(ctx context.Context, token, key, id string, permissions []string) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	if c.Context.Principal.ID == "" {
		return ErrForbidden
	}
	if id == "" || len(id) > 128 || len(permissions) == 0 {
		return ErrInvalid
	}
	if _, builtin := BuiltinRoles()[id]; builtin {
		return ErrForbidden
	}
	for _, p := range permissions {
		if len(strings.Split(p, ":")) != 3 {
			return ErrInvalid
		}
	}
	r := s.root("core", "role", id)
	return s.append(ctx, s.event(r, "create", key, 0, RoleState{ID: id, Network: s.Network, Permissions: permissions, Principal: c.Context.Principal, Subject: c.Context.Principal}))
}

// ChangeRolePermissions preserves role ownership and forbids expanding an in-use role.
// Grant holders must never gain permissions through someone editing their role definition.
func (s *Service) ChangeRolePermissions(ctx context.Context, token, key, id string, permissions []string, remove bool) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	if _, builtin := BuiltinRoles()[id]; builtin {
		return ErrForbidden
	}
	r := s.root("core", "role", id)
	var role RoleState
	a, e := s.load(ctx, r, &role)
	if e != nil {
		return e
	}
	if a.Version == 0 || role.Principal != c.Context.Principal {
		return ErrForbidden
	}
	for _, p := range permissions {
		if len(strings.Split(p, ":")) != 3 {
			return ErrInvalid
		}
	}
	if !remove {
		principals, e := s.Store.Query(ctx, s.Network, "core", "principal", []eventstore.Predicate{{Path: "roles.id", Value: mustJSON(id)}})
		if e != nil {
			return e
		}
		for _, p := range principals {
			var st PrincipalState
			if e = json.Unmarshal(p.State, &st); e != nil {
				return e
			}
			for _, g := range st.Roles {
				if g.ID == id {
					return errors.New("cannot expand a granted role")
				}
			}
		}
	}
	action := "add-permissions"
	if remove {
		action = "remove-permissions"
	}
	return s.append(ctx, s.event(r, action, key, a.Version, map[string]any{"permissions": permissions}))
}

// StreamGroupPrincipals applies the same authorization as the bounded list fact.
func (s *Service) StreamGroupPrincipals(ctx context.Context, token string, group Ref, visit func(Ref) error) error {
	if visit == nil {
		return ErrInvalid
	}
	principals, e := s.GroupPrincipals(ctx, token, group)
	if e != nil {
		return e
	}
	for _, p := range principals {
		if e = ctx.Err(); e != nil {
			return e
		}
		if e = visit(p); e != nil {
			return e
		}
	}
	return nil
}

func (s *Service) AddRoles(ctx context.Context, token, key string, principal Ref, roles []RoleGrant) error {
	return s.ChangeRoles(ctx, token, key, principal, roles, false)
}
func (s *Service) RemoveRoles(ctx context.Context, token, key string, principal Ref, roles []RoleGrant, subjects []Ref) error {
	if validKey(key) != nil || principal.ID == "" || principal.Network != s.Network || principal.Service != "core" || principal.Domain != "principal" {
		return ErrInvalid
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	for _, subject := range subjects {
		if !validRef(subject) || subject.Network != s.Network {
			return ErrInvalid
		}
	}
	// Bind the original intent before expanding subjects from mutable state. The
	// session-scoped key prevents a different caller from recovering this action.
	request := mustJSON(struct {
		Session   Ref         `json:"session"`
		Caller    Ref         `json:"caller"`
		Principal Ref         `json:"principal"`
		Roles     []RoleGrant `json:"roles"`
		Subjects  []Ref       `json:"subjects"`
	}{c.Context.Session, c.Context.Principal, principal, roles, subjects})
	request, e = eventstore.Canonical(request)
	if e != nil {
		return e
	}
	key = stableID(c.Context.Session.Key(), "remove-roles-request/"+key)
	receipt := s.root("core", "operation", stableID(key, "remove-roles-receipt"))
	committed := func() (bool, error) {
		var op operationState
		a, err := s.load(ctx, receipt, &op)
		if err != nil || a.Version == 0 {
			return false, err
		}
		if string(op.Request) != string(request) || op.Digest != requestDigest(request) {
			return false, eventstore.ErrIdempotency
		}
		// No replay: Inputs are the exact original prepared targets, committed in
		// the same transaction as this receipt. Later regrants remain untouched.
		if len(op.Inputs) == 0 || op.Inputs[0].Root != principal || op.Inputs[0].Action != "remove-roles" {
			return false, ErrInvalid
		}
		return true, nil
	}
	if done, err := committed(); done || err != nil {
		return err
	}
	var state PrincipalState
	if _, e := s.load(ctx, principal, &state); e != nil {
		return e
	}
	// Copy before expansion so caller-owned slices cannot change the request.
	roles = append([]RoleGrant(nil), roles...)
	for _, g := range state.Roles {
		for _, subject := range subjects {
			if g.Subject == subject {
				roles = append(roles, g)
				break
			}
		}
	}
	if len(roles) == 0 {
		if done, err := committed(); done || err != nil {
			return err
		}
		return ErrForbidden
	}
	e = s.changeRoles(ctx, token, key, principal, roles, true, request, receipt)
	if e != nil {
		// A concurrent identical removal may have consumed our authority between
		// the first lookup and authorization. Only its exact receipt may recover.
		if done, err := committed(); done || err != nil {
			return err
		}
	}
	return e
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
func (s *Service) groupsForPrincipal(ctx context.Context, principal Ref) ([]eventstore.Aggregate, error) {
	return s.Store.Query(ctx, s.Network, "core", "group", []eventstore.Predicate{{Path: "principals.id", Value: mustJSON(principal.ID)}, {Path: "principals.service", Value: mustJSON(principal.Service)}, {Path: "principals.domain", Value: mustJSON(principal.Domain)}, {Path: "principals.network", Value: mustJSON(principal.Network)}})
}
