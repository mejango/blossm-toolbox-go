package dia

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"blossm.local/backend/eventstore"
)

var (
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrInvalid      = errors.New("invalid input")
	ErrUnavailable  = errors.New("required adapter unavailable")
)

// Store is the consumer contract. Append must atomically validate and commit the entire batch.
type Store interface {
	Append(context.Context, []eventstore.Input) ([]eventstore.Receipt, error)
	Aggregate(context.Context, eventstore.Root) (eventstore.Aggregate, error)
	Query(context.Context, string, string, string, []eventstore.Predicate) ([]eventstore.Aggregate, error)
}
type Service struct {
	Store         Store
	JWT           *JWT
	Network       string
	Mail          Mailer
	SMS           SMSSender
	Updates       UpdatesPublisher
	EncryptionKey []byte
	HMACKey       []byte
	Now           func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *Service) root(service, domain, id string) Ref {
	return Ref{Network: s.Network, Service: service, Domain: domain, ID: id}
}
func stableID(key, label string) string {
	h := sha256.Sum256([]byte(key + "\x00" + label))
	return hex.EncodeToString(h[:])
}
func validKey(key string) error {
	if strings.TrimSpace(key) == "" || len(key) > 256 {
		return ErrInvalid
	}
	return nil
}
func same(a, b Ref) bool { return a == b }

// load adapts the store last-event version (-1 absent) to the next expected count.
func (s *Service) load(ctx context.Context, r Ref, out any) (eventstore.Aggregate, error) {
	if s.Store == nil {
		return eventstore.Aggregate{}, ErrUnavailable
	}
	a, e := s.Store.Aggregate(ctx, r)
	if e != nil {
		return a, e
	}
	a.Version++
	if len(a.State) > 0 && string(a.State) != "null" {
		e = decode(a.State, out)
	}
	return a, e
}
func (s *Service) event(r Ref, action, key string, version int64, payload any) eventstore.Input {
	b, e := json.Marshal(payload)
	if e != nil {
		panic(e)
	}
	// Event timestamps reflect preparation time; persisted operation receipts retain
	// the original inputs for exact retries.
	key = stableID(r.Key(), action+"/"+key)
	return eventstore.Input{Root: r, Action: action, Key: key, Expected: &version, Created: s.now().Format(time.RFC3339Nano), Payload: b, Transaction: eventstore.Transaction{ID: key, Issued: s.now().Format(time.RFC3339Nano)}}
}
func (s *Service) append(ctx context.Context, events ...eventstore.Input) error {
	if s.Store == nil {
		return ErrUnavailable
	}
	return s.appendAtomic(ctx, events)
}
func (s *Service) authenticate(ctx context.Context, token, kind string) (Claims, error) {
	if s.JWT == nil {
		return Claims{}, ErrUnavailable
	}
	c, e := s.JWT.Verify(token, kind)
	if e != nil {
		return c, ErrUnauthorized
	}
	if c.Context.Network != s.Network {
		return c, ErrUnauthorized
	}
	if kind == "access" || kind == "challenge" || kind == "updates" {
		if e := s.authorizeSessionContext(ctx, c, kind == "challenge"); e != nil {
			return c, e
		}
	}
	if kind == "connection" || kind == "connection-updates" {
		if _, e := s.connectionKey(ctx, c); e != nil {
			return c, e
		}
	}
	return c, nil
}

// authorizeSessionContext owns live session/identity/scene validation. Challenges
// may carry the pre-upgrade identity, but still require the original live session.
func (s *Service) authorizeSessionContext(ctx context.Context, c Claims, challenge bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Context.Network != s.Network || c.Context.Session.ID == "" || c.Context.Session != s.root("core", "session", c.Context.Session.ID) {
		return ErrUnauthorized
	}
	var st SessionState
	a, err := s.load(ctx, c.Context.Session, &st)
	if err != nil || a.Version == 0 || st.Terminated != "" || st.Expires <= s.now().Unix() || c.Expires > st.Expires || c.Expires <= s.now().Unix() || c.NotBefore > s.now().Unix() {
		return ErrUnauthorized
	}
	if challenge {
		return nil
	}
	for domain, ref := range map[string]Ref{"principal": c.Context.Principal, "account": c.Context.Account, "scene": c.Context.Scene} {
		if ref != (Ref{}) && (ref.ID == "" || ref != s.root("core", domain, ref.ID)) {
			return ErrUnauthorized
		}
	}
	expectedSubject := st.Principal.ID
	if expectedSubject == "" {
		expectedSubject = c.Context.Session.ID
	}
	if c.Subject != expectedSubject || st.Principal != c.Context.Principal || st.Account != c.Context.Account || st.Scene != c.Context.Scene {
		return ErrUnauthorized
	}
	if c.Context.Subject != (Ref{}) && (!validRef(c.Context.Subject) || c.Context.Subject.Network != s.Network) {
		return ErrUnauthorized
	}
	if c.Context.Scene.ID != "" {
		var scene SceneState
		a, e := s.load(ctx, c.Context.Scene, &scene)
		if e != nil || a.Version == 0 || scene.Deleted != "" || scene.Subject != c.Context.Subject {
			return ErrUnauthorized
		}
	} else if c.Context.Subject != (Ref{}) {
		return ErrUnauthorized
	}
	return nil
}
func BuiltinRoles() map[string][]string {
	return map[string][]string{
		"AccountAdmin": {"core:account:create", "core:account:update"}, "GroupAdmin": {"core:group:update", "core:group:read"}, "GroupMember": {"core:group:read"}, "SceneAdmin": {"core:scene:delete", "core:scene:read"}, "SceneViewer": {"core:scene:read"}, "NodeAdmin": {"system:node:update", "system:node:delete", "system:key:create", "system:key:read"}, "NodeNetwork": {"system:connection:create", "system:updates:create"},
	}
}
func (s *Service) require(ctx context.Context, c Claims, subject Ref, permission string) error {
	if c.Context.Principal.ID == "" || subject.Network != s.Network {
		return ErrForbidden
	}
	var p PrincipalState
	_, e := s.load(ctx, c.Context.Principal, &p)
	if e != nil {
		return e
	}
	for _, g := range p.Roles {
		if !same(g.Subject, subject) {
			continue
		}
		perms, ok := BuiltinRoles()[g.ID]
		if !ok {
			var role RoleState
			_, e := s.load(ctx, s.root("core", "role", g.ID), &role)
			if e != nil {
				return e
			}
			perms = role.Permissions
		}
		for _, v := range perms {
			if v == permission {
				return nil
			}
		}
	}
	return ErrForbidden
}
func (s *Service) sign(ctx context.Context, c Claims, kind string, sc SessionContext, expiry int64) (string, error) {
	if s.JWT == nil {
		return "", ErrUnavailable
	}
	subject := sc.Principal.ID
	if subject == "" {
		subject = sc.Session.ID
	}
	return s.JWT.Sign(ctx, Claims{Issuer: s.JWT.Issuer, Audience: []string{s.Network}, Subject: subject, ID: c.ID, Kind: kind, NotBefore: s.now().Unix(), Expires: expiry, Context: sc})
}
func (s *Service) emailID(email string) (string, error) {
	if len(s.HMACKey) < 32 {
		return "", fmt.Errorf("%w: email HMAC key", ErrUnavailable)
	}
	h := hmac.New(sha256.New, s.HMACKey)
	h.Write([]byte(email))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// AuthenticateAccess verifies cryptographic claims and current persisted session identity.
// Transport and wallet callers must use this instead of JWT.Verify alone.
func (s *Service) AuthenticateAccess(ctx context.Context, token string) (Claims, error) {
	return s.authenticate(ctx, token, "access")
}

// recoverAccess is used only after an operation-specific deterministic receipt
// proves the expected transition. It cannot refresh an arbitrary current identity.
func (s *Service) recoverAccess(ctx context.Context, oldToken string, expected SessionContext) (string, error) {
	if s.JWT == nil {
		return "", ErrUnavailable
	}
	c, e := s.JWT.Verify(oldToken, "access")
	if e != nil || c.Context.Session != expected.Session || c.Context.Network != expected.Network {
		return "", ErrUnauthorized
	}
	var state SessionState
	a, e := s.load(ctx, expected.Session, &state)
	if e != nil || a.Version == 0 || state.Terminated != "" || state.Principal != expected.Principal || state.Account != expected.Account || state.Scene != expected.Scene || state.Expires < c.Expires {
		return "", ErrUnauthorized
	}
	return s.sign(ctx, c, "access", expected, c.Expires)
}
