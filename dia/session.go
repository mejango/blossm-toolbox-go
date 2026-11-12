package dia

import (
	"blossm.local/backend/eventstore"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/mail"
	"strings"
)

type SessionResult struct {
	Context SessionContext `json:"context"`
	Token   string         `json:"token"`
	Session Ref            `json:"session"`
}

// NewSessionSecret supplies an unguessable creation credential. Keep it secret.
func NewSessionSecret() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Service) StartSession(ctx context.Context, key string, device Device) (SessionResult, error) {
	if !validDevice(device) || device.Type == "" || device.Platform == "" || strings.TrimSpace(device.Agent) == "" || len(device.Agent) > 1024 {
		return SessionResult{}, ErrInvalid
	}
	secret, e := base64.RawURLEncoding.DecodeString(key)
	if e != nil || len(secret) != 32 {
		return SessionResult{}, ErrInvalid
	}
	r := s.root("core", "session", stableID(key, "session"))
	var state SessionState
	a, e := s.load(ctx, r, &state)
	if e != nil && !errors.Is(e, eventstore.ErrNotFound) {
		return SessionResult{}, e
	}
	if a.Version == 0 {
		state = SessionState{Device: device, Started: s.now().Unix(), Expires: s.now().Add(90 * 24 * 60 * 60 * 1e9).Unix()}
		if e = s.append(ctx, s.event(r, "start", key+"/start", 0, state)); e != nil {
			return SessionResult{}, e
		}
	} else if state.Device != device {
		return SessionResult{}, eventstore.ErrIdempotency
	}
	if state.Principal.ID != "" || state.Account.ID != "" || state.Scene.ID != "" {
		return SessionResult{}, ErrUnauthorized
	}
	if state.Terminated != "" || state.Expires <= s.now().Unix() {
		return SessionResult{}, ErrUnauthorized
	}
	sc := SessionContext{Network: s.Network, Session: r, Device: state.Device, Principal: state.Principal, Account: state.Account, Scene: state.Scene}
	token, e := s.sign(ctx, Claims{ID: stableID(key, "token")}, "access", sc, state.Expires)
	return SessionResult{Context: sc, Token: token, Session: r}, e
}
func (s *Service) TerminateSession(ctx context.Context, token, key string) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		if s.JWT == nil {
			return ErrUnavailable
		}
		prior, verifyErr := s.JWT.Verify(token, "access")
		if verifyErr != nil || prior.Context.Network != s.Network {
			return ErrUnauthorized
		}
		_, committed, lookupErr := s.operation(ctx, prior.Context.Session, "terminate", key)
		if lookupErr != nil {
			return lookupErr
		}
		if committed {
			return nil
		}
		return e
	}
	var state SessionState
	a, e := s.load(ctx, c.Context.Session, &state)
	if e != nil {
		return e
	}
	return s.append(ctx, s.event(a.Root, "terminate", key, a.Version, map[string]string{"terminated": s.now().Format("2006-01-02T15:04:05.999999999Z07:00")}))
}
func (s *Service) SessionTerminated(ctx context.Context, token string) (bool, error) {
	if s.JWT == nil {
		return false, ErrUnavailable
	}
	c, e := s.JWT.Verify(token, "access")
	if e != nil {
		return false, ErrUnauthorized
	}
	var st SessionState
	a, e := s.load(ctx, c.Context.Session, &st)
	return st.Terminated != "" || a.Version == 0, e
}
func (s *Service) SetPassword(ctx context.Context, token, key, password string) error {
	if e := validKey(key); e != nil {
		return e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return e
	}
	if e = s.require(ctx, c, c.Context.Account, "core:account:update"); e != nil {
		return e
	}
	var st AccountState
	a, e := s.load(ctx, c.Context.Account, &st)
	if e != nil {
		return e
	}
	if st.Password != "" {
		op, committed, lookupErr := s.operation(ctx, a.Root, "set-password", key)
		if lookupErr != nil {
			return lookupErr
		}
		if committed && len(op.Inputs) > 0 {
			var prior struct {
				Password string `json:"password"`
			}
			if decode(op.Inputs[0].Payload, &prior) == nil && CheckPassword(password, prior.Password) {
				return nil
			}
			return eventstore.ErrIdempotency
		}
		return errors.New("password already set")
	}
	hash, e := HashPassword(password)
	if e != nil {
		return e
	}
	return s.append(ctx, s.event(a.Root, "set-password", key, a.Version, map[string]string{"password": hash}))
}
func (s *Service) ChangeScene(ctx context.Context, token, key string, scene Ref) (SessionResult, error) {
	if e := validKey(key); e != nil {
		return SessionResult{}, e
	}
	c, e := s.authenticate(ctx, token, "access")
	recovering := e != nil
	if recovering {
		if s.JWT == nil {
			return SessionResult{}, ErrUnavailable
		}
		c, e = s.JWT.Verify(token, "access")
		if e != nil || c.Context.Network != s.Network {
			return SessionResult{}, ErrUnauthorized
		}
		op, committed, lookupErr := s.operation(ctx, c.Context.Session, "change-scene", key)
		if lookupErr != nil {
			return SessionResult{}, lookupErr
		}
		if !committed || len(op.Inputs) != 1 {
			return SessionResult{}, ErrUnauthorized
		}
		var prior struct {
			Scene Ref `json:"scene"`
		}
		if decode(op.Inputs[0].Payload, &prior) != nil || prior.Scene != scene {
			return SessionResult{}, eventstore.ErrIdempotency
		}
	}

	var st SceneState
	a, e := s.load(ctx, scene, &st)
	if e != nil {
		return SessionResult{}, e
	}
	if a.Version == 0 || st.Deleted != "" {
		return SessionResult{}, ErrForbidden
	}
	if e = s.require(ctx, c, scene, "core:scene:read"); e != nil {
		return SessionResult{}, e
	}
	var group GroupState
	_, e = s.load(ctx, st.Group, &group)
	if e != nil {
		return SessionResult{}, e
	}
	member := false
	for _, p := range group.Principals {
		member = member || same(p, c.Context.Principal)
	}
	if !member {
		return SessionResult{}, ErrForbidden
	}
	var session SessionState
	sa, e := s.load(ctx, c.Context.Session, &session)
	if e != nil {
		return SessionResult{}, e
	}
	if !recovering {
		if e = s.append(ctx, s.event(sa.Root, "change-scene", key, sa.Version, map[string]Ref{"scene": scene})); e != nil {
			return SessionResult{}, e
		}
	}
	sc := c.Context
	sc.Scene = scene
	sc.Subject = st.Subject
	var t string
	if recovering {
		t, e = s.recoverAccess(ctx, token, sc)
	} else {
		t, e = s.sign(ctx, c, "access", sc, c.Expires)
	}
	return SessionResult{Context: sc, Token: t, Session: sc.Session}, e
}
func (s *Service) DeletedScene(ctx context.Context, token string, scene Ref) (bool, error) {
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return false, e
	}
	if e = s.require(ctx, c, scene, "core:scene:read"); e != nil {
		return false, e
	}
	var st SceneState
	a, e := s.load(ctx, scene, &st)
	return st.Deleted != "" || a.Version == 0, e
}

// PublicKey exposes only the public verifier material.
func (s *Service) PublicKey() ([]byte, error) {
	if s.JWT == nil {
		return nil, ErrUnavailable
	}
	return s.JWT.PublicKey()
}
func normalizeEmail(v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	address, err := mail.ParseAddress(v)
	if err != nil || address.Address != v || address.Name != "" {
		return "", ErrInvalid
	}
	parts := strings.Split(v, "@")
	if len(parts) != 2 || parts[0] == "" || !strings.Contains(parts[1], ".") || strings.ContainsAny(v, "\r\n\t ") || len(v) > 254 {
		return "", ErrInvalid
	}
	return v, nil
}
