package dia

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"blossm.local/backend/eventstore"
)

// decode is the shared typed boundary for commands and persisted domain data.
func decode(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("JSON object required")
	}
	return strictJSON(raw, dst)
}

type domainAction[S any] struct {
	normalize func(json.RawMessage) (json.RawMessage, error)
	apply     func(*S, json.RawMessage) error
}
type domainReducer[S any] map[string]domainAction[S]

func (r domainReducer[S]) Normalize(action string, raw json.RawMessage) (json.RawMessage, error) {
	a, ok := r[action]
	if !ok {
		return nil, fmt.Errorf("unsupported action %q", action)
	}
	return a.normalize(raw)
}
func (r domainReducer[S]) Apply(raw json.RawMessage, event eventstore.Event) (json.RawMessage, error) {
	payload, err := r.Normalize(event.Input.Action, event.Input.Payload)
	if err != nil {
		return nil, err
	}
	var state S
	if len(raw) != 0 {
		if err := decode(raw, &state); err != nil {
			return nil, fmt.Errorf("invalid state: %w", err)
		}
	}
	if err := r[event.Input.Action].apply(&state, payload); err != nil {
		return nil, err
	}
	return json.Marshal(state)
}
func action[S, P any](validate func(P) error, apply func(*S, P) error) domainAction[S] {
	return domainAction[S]{
		normalize: func(raw json.RawMessage) (json.RawMessage, error) {
			var p P
			if err := decode(raw, &p); err != nil {
				return nil, err
			}
			if err := validate(p); err != nil {
				return nil, err
			}
			return json.Marshal(p)
		},
		apply: func(s *S, raw json.RawMessage) error {
			var p P
			if err := decode(raw, &p); err != nil {
				return err
			}
			return apply(s, p)
		},
	}
}
func replace[S any](validate func(S) error) domainAction[S] {
	return action(validate, func(s *S, p S) error { *s = p; return nil })
}
func require(ok bool, message string) error {
	if !ok {
		return errors.New(message)
	}
	return nil
}
func nonempty(s string) bool { return strings.TrimSpace(s) != "" }
func validRef(r Ref) bool {
	return nonempty(r.ID) && nonempty(r.Network) && nonempty(r.Service) && nonempty(r.Domain)
}
func optionalRef(r Ref) bool { return r == (Ref{}) || validRef(r) }
func validDevice(d Device) bool {
	return (d.Type == "" || d.Type == "mobile" || d.Type == "desktop" || d.Type == "tablet") && (d.Platform == "" || d.Platform == "ios" || d.Platform == "android" || d.Platform == "browser")
}
func validContext(c SessionContext) bool {
	return optionalRef(c.Session) && optionalRef(c.Principal) && optionalRef(c.Account) && optionalRef(c.Scene) && optionalRef(c.Subject) && validDevice(c.Device)
}
func validStrings(ss []string) bool {
	if ss == nil {
		return false
	}
	for _, s := range ss {
		if !nonempty(s) {
			return false
		}
	}
	return true
}
func validRefs(rs []Ref) bool {
	if rs == nil {
		return false
	}
	for _, r := range rs {
		if !validRef(r) {
			return false
		}
	}
	return true
}
func validRoles(rs []RoleGrant) bool {
	if rs == nil {
		return false
	}
	for _, r := range rs {
		if !nonempty(r.ID) || !validRef(r.Subject) {
			return false
		}
	}
	return true
}

// set preserves insertion order and compares complete identities, without delimiter encoding.
func set[T comparable](old, items []T, remove bool) []T {
	result := make([]T, 0, len(old)+len(items))
	seen := make(map[T]bool)
	if remove {
		for _, v := range items {
			seen[v] = true
		}
	}
	for _, v := range old {
		if !seen[v] {
			result = append(result, v)
			seen[v] = true
		}
	}
	if !remove {
		for _, v := range items {
			if !seen[v] {
				result = append(result, v)
				seen[v] = true
			}
		}
	}
	return result
}

type revokedPayload struct {
	Revoked string `json:"revoked"`
}

type passwordPayload struct {
	Password string `json:"password"`
}
type answeredPayload struct {
	AnswerKey string `json:"answerKey"`
	ProofHash string `json:"proofHash"`
	Answered  string `json:"answered"`
}
type deletedPayload struct {
	Deleted string `json:"deleted"`
}
type terminatedPayload struct {
	Terminated string `json:"terminated"`
}
type subjectPayload struct {
	Subject Ref `json:"subject"`
}
type rolesPayload struct {
	Roles []RoleGrant `json:"roles"`
}
type principalsPayload struct {
	Principals []Ref `json:"principals"`
}
type networksPayload struct {
	Networks []string `json:"networks"`
}
type permissionsPayload struct {
	Permissions []string `json:"permissions"`
}
type upgradePayload struct {
	Principal *Ref `json:"principal,omitempty"`
	Account   *Ref `json:"account,omitempty"`
	Scene     *Ref `json:"scene,omitempty"`
}
type scenePayload struct {
	Scene Ref `json:"scene"`
}
type smsPayload struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

// commsReducer keeps provider payload events separate from encrypted preparations.
// A prepared delivery can only be created once; retries read the stored payload.
type commsReducer struct {
	legacy  eventstore.Reducer
	prepare eventstore.Reducer
}

func (r commsReducer) Normalize(action string, raw json.RawMessage) (json.RawMessage, error) {
	if action == "prepare" {
		return r.prepare.Normalize(action, raw)
	}
	return r.legacy.Normalize(action, raw)
}
func (r commsReducer) Apply(state json.RawMessage, event eventstore.Event) (json.RawMessage, error) {
	if event.Input.Action == "prepare" {
		if event.Number != 0 {
			return nil, errors.New("delivery preparation must be the first event")
		}
		return r.prepare.Apply(state, event)
	}
	return r.legacy.Apply(state, event)
}
func encryptedComms(legacy eventstore.Reducer) eventstore.Reducer {
	return commsReducer{legacy: legacy, prepare: domainReducer[CommsState]{
		"prepare": action(func(p CommsState) error {
			return require(nonempty(p.Delivery) && nonempty(p.RequestHash) && validRef(p.Subject), "invalid delivery preparation")
		}, func(s *CommsState, p CommsState) error {
			if *s != (CommsState{}) {
				return errors.New("delivery already prepared")
			}
			*s = p
			return nil
		}),
	}}
}

// Reducers returns independent domain registries. Authentication and delivery belong to services.
func Reducers() map[string]eventstore.Reducer {
	session := domainReducer[SessionState]{
		"start": action(func(p SessionState) error {
			return require(validDevice(p.Device) && p.Started > 0 && p.Expires > p.Started && p.Principal == (Ref{}) && p.Account == (Ref{}) && p.Scene == (Ref{}) && p.Terminated == "", "invalid session start")
		}, func(s *SessionState, p SessionState) error {
			if s.Terminated != "" {
				return errors.New("session is terminated")
			}
			s.Device = p.Device
			s.Started = p.Started
			s.Expires = p.Expires
			return nil
		}),
		"upgrade": action(func(p upgradePayload) error {
			if p.Principal == nil && p.Account == nil && p.Scene == nil {
				return errors.New("empty session upgrade")
			}
			for _, r := range []*Ref{p.Principal, p.Account, p.Scene} {
				if r != nil && !validRef(*r) {
					return errors.New("invalid session reference")
				}
			}
			return nil
		}, func(s *SessionState, p upgradePayload) error {
			if s.Terminated != "" {
				return errors.New("session is terminated")
			}
			if p.Principal != nil {
				if s.Account != (Ref{}) && s.Principal != (Ref{}) && s.Principal != *p.Principal {
					return errors.New("cannot switch session principal")
				}
				s.Principal = *p.Principal
			}
			if p.Account != nil {
				if s.Account != (Ref{}) && s.Account != *p.Account {
					return errors.New("cannot switch session account")
				}
				s.Account = *p.Account
			}
			if p.Scene != nil {
				s.Scene = *p.Scene
			}
			return nil
		}),
		"change-scene": action(func(p scenePayload) error { return require(validRef(p.Scene), "invalid scene") }, func(s *SessionState, p scenePayload) error {
			if s.Terminated != "" {
				return errors.New("session is terminated")
			}
			s.Scene = p.Scene
			return nil
		}),
		"terminate": action(func(p terminatedPayload) error { return require(nonempty(p.Terminated), "termination required") }, func(s *SessionState, p terminatedPayload) error {
			if s.Terminated != "" && s.Terminated != p.Terminated {
				return errors.New("session already terminated")
			}
			s.Terminated = p.Terminated
			return nil
		}),
	}
	account := domainReducer[AccountState]{
		"register": replace(func(p AccountState) error {
			return require(nonempty(p.EmailHMAC) && nonempty(p.EmailEncrypted) && validRef(p.Principal), "invalid account")
		}),
		"set-password": action(func(p passwordPayload) error { return require(nonempty(p.Password), "password required") }, func(s *AccountState, p passwordPayload) error { s.Password = p.Password; return nil }),
	}
	challenge := domainReducer[ChallengeState]{
		"recover": action(func(p struct {
			Attempts int    `json:"attempts"`
			Hash     string `json:"hash"`
		}) error {
			return require(p.Attempts > 0 && p.Attempts <= MaxChallengeAttempts && len(p.Hash) == 64, "invalid recovery attempt")
		}, func(s *ChallengeState, p struct {
			Attempts int    `json:"attempts"`
			Hash     string `json:"hash"`
		}) error {
			if s.Answered == "" || p.Attempts != s.RecoveryAttempts+1 {
				return errors.New("invalid recovery transition")
			}
			s.RecoveryAttempts = p.Attempts
			return nil
		}),
		"attempt": action(func(p struct {
			Attempts int    `json:"attempts"`
			Hash     string `json:"hash"`
		}) error {
			return require(p.Attempts > 0 && p.Attempts <= MaxChallengeAttempts && len(p.Hash) == 64, "invalid attempt")
		}, func(s *ChallengeState, p struct {
			Attempts int    `json:"attempts"`
			Hash     string `json:"hash"`
		}) error {
			if s.Answered != "" || p.Attempts != s.Attempts+1 {
				return errors.New("invalid challenge attempt transition")
			}
			s.Attempts = p.Attempts
			return nil
		}),
		"issue": action(func(p ChallengeState) error {
			return require(nonempty(p.Code) && p.Expires > 0 && p.Attempts == 0 && p.Answered == "" && validRef(p.Session) && validContext(p.Upgrade), "invalid challenge")
		}, func(s *ChallengeState, p ChallengeState) error {
			if s.Answered != "" {
				return errors.New("challenge already answered")
			}
			*s = p
			return nil
		}),
		"answer": action(func(p answeredPayload) error { return require(nonempty(p.Answered), "answer required") }, func(s *ChallengeState, p answeredPayload) error {
			if s.Answered != "" {
				return errors.New("challenge already answered")
			}
			s.Answered = p.Answered
			s.AnswerKey = p.AnswerKey
			s.ProofHash = p.ProofHash
			return nil
		}),
	}
	principal := domainReducer[PrincipalState]{
		"set-subject": action(func(p subjectPayload) error { return require(validRef(p.Subject), "invalid subject") }, func(s *PrincipalState, p subjectPayload) error { s.Subject = p.Subject; return nil }),
	}
	group := domainReducer[GroupState]{}
	role := domainReducer[RoleState]{"touch": action(func(p struct{}) error { return nil }, func(s *RoleState, p struct{}) error { return nil }), "create": action(func(p RoleState) error {
		return require(nonempty(p.ID) && nonempty(p.Network) && validStrings(p.Permissions) && validRef(p.Principal) && validRef(p.Subject), "invalid role or missing ownership")
	}, func(s *RoleState, p RoleState) error {
		p.Permissions = set[string](nil, p.Permissions, false)
		*s = p
		return nil
	})}
	for _, remove := range []bool{false, true} {
		prefix := "add-"
		if remove {
			prefix = "remove-"
		}
		principal[prefix+"roles"] = action(func(p rolesPayload) error { return require(validRoles(p.Roles), "invalid roles") }, func(s *PrincipalState, p rolesPayload) error { s.Roles = set(s.Roles, p.Roles, remove); return nil })
		group[prefix+"principals"] = action(func(p principalsPayload) error { return require(validRefs(p.Principals), "invalid principals") }, func(s *GroupState, p principalsPayload) error {
			s.Principals = set(s.Principals, p.Principals, remove)
			return nil
		})
		group[prefix+"networks"] = action(func(p networksPayload) error { return require(validStrings(p.Networks), "invalid networks") }, func(s *GroupState, p networksPayload) error {
			s.Networks = set(s.Networks, p.Networks, remove)
			return nil
		})
		role[prefix+"permissions"] = action(func(p permissionsPayload) error { return require(validStrings(p.Permissions), "invalid permissions") }, func(s *RoleState, p permissionsPayload) error {
			s.Permissions = set(s.Permissions, p.Permissions, remove)
			return nil
		})
	}
	scene := domainReducer[SceneState]{
		"register": action(func(p SceneState) error {
			return require(validRef(p.Subject) && validRef(p.Group) && p.Deleted == "", "invalid scene")
		}, func(s *SceneState, p SceneState) error {
			if s.Deleted != "" {
				return errors.New("scene is deleted")
			}
			*s = p
			return nil
		}),
		"delete": action(func(p deletedPayload) error { return require(nonempty(p.Deleted), "deletion required") }, func(s *SceneState, p deletedPayload) error {
			if s.Deleted != "" && s.Deleted != p.Deleted {
				return errors.New("scene already deleted")
			}
			s.Deleted = p.Deleted
			return nil
		}),
	}
	return map[string]eventstore.Reducer{
		"core/operation": operationReducer{},
		"core/session":   session, "core/account": account, "core/challenge": challenge,
		"core/principal": principal, "core/group": group, "core/role": role, "core/scene": scene,
		"system/node": domainReducer[NodeState]{"register": replace(func(p NodeState) error { return require(nonempty(p.Network) && validRef(p.Scene), "invalid node") })},
		"system/key": domainReducer[KeyState]{
			"create": action(func(p KeyState) error {
				return require(nonempty(p.Name) && nonempty(p.Network) && nonempty(p.Secret) && validRef(p.Scene) && validRef(p.Principal) && validRef(p.Subject) && p.Revoked == "", "invalid key")
			}, func(s *KeyState, p KeyState) error {
				if s.Revoked != "" {
					return errors.New("key is revoked")
				}
				*s = p
				return nil
			}),
			"revoke": action(func(p revokedPayload) error { return require(nonempty(p.Revoked), "revocation required") }, func(s *KeyState, p revokedPayload) error {
				if s.Revoked != "" && s.Revoked != p.Revoked {
					return errors.New("key already revoked")
				}
				s.Revoked = p.Revoked
				return nil
			}),
		},
		"comms/email": encryptedComms(domainReducer[Mail]{"send-auth-code": replace(func(p Mail) error {
			return require(nonempty(p.To) && nonempty(p.From) && nonempty(p.Subject) && nonempty(p.Body), "invalid mail")
		})}),
		"comms/sms": encryptedComms(domainReducer[smsPayload]{"send": replace(func(p smsPayload) error { return require(nonempty(p.To) && nonempty(p.Message), "invalid SMS") })}),
	}
}
