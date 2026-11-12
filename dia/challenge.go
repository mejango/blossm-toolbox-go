package dia

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"math/big"
	"time"
)

// MaxChallengeAttempts is the explicit per-challenge proof-attempt policy.
const MaxChallengeAttempts = 5

type ChallengeResult struct {
	Challenge Ref    `json:"challenge"`
	Token     string `json:"token"`
	Expires   int64  `json:"expires"`
}

// SaveSession prepares account creation or login. No account/role changes occur until AnswerChallenge.
func (s *Service) SaveSession(ctx context.Context, token, key, email, password string) (ChallengeResult, error) {
	if e := validKey(key); e != nil {
		return ChallengeResult{}, e
	}
	c, e := s.authenticate(ctx, token, "access")
	if e != nil {
		return ChallengeResult{}, e
	}
	email, e = normalizeEmail(email)
	if e != nil {
		return ChallengeResult{}, e
	}
	if len(s.HMACKey) < 32 {
		return ChallengeResult{}, ErrUnavailable
	}
	intent := hmac.New(sha256.New, s.HMACKey)
	intent.Write([]byte(email + "\x00" + password))
	requestHash := hex.EncodeToString(intent.Sum(nil))
	cr := s.root("core", "challenge", stableID(c.Context.Session.Key()+key, "challenge"))
	var existing ChallengeState
	ca, e := s.load(ctx, cr, &existing)
	if e != nil {
		return ChallengeResult{}, e
	}
	if ca.Version > 0 {
		if existing.RequestHash != requestHash {
			return ChallengeResult{}, eventstore.ErrIdempotency
		}
		return s.challengeResult(ctx, c, cr, existing)
	}
	hid, e := s.emailID(email)
	if e != nil {
		return ChallengeResult{}, e
	}
	ar := s.root("core", "account", hid)
	var account AccountState
	aa, e := s.load(ctx, ar, &account)
	if e != nil {
		return ChallengeResult{}, e
	}
	var events []eventstore.Input
	principal := account.Principal
	if aa.Version == 0 {
		principal = c.Context.Principal
		if principal.ID == "" {
			principal = s.root("core", "principal", stableID(ar.Key(), "principal"))
		}
		var p PrincipalState
		pa, e := s.load(ctx, principal, &p)
		if e != nil {
			return ChallengeResult{}, e
		}
		if p.Subject.ID != "" {
			return ChallengeResult{}, ErrForbidden
		}
		encrypted, e := Seal(s.EncryptionKey, []byte(email))
		if e != nil {
			return ChallengeResult{}, e
		}
		account = AccountState{EmailHMAC: hid, EmailEncrypted: base64.RawURLEncoding.EncodeToString(encrypted), Principal: principal}
		if password != "" {
			account.Password, e = HashPassword(password)
			if e != nil {
				return ChallengeResult{}, e
			}
		}
		events = append(events, s.event(ar, "register", key+"/account", 0, account), s.event(principal, "set-subject", key+"/subject", pa.Version, map[string]Ref{"subject": ar}), s.event(principal, "add-roles", key+"/account-role", pa.Version+1, map[string]any{"roles": []RoleGrant{{ID: "AccountAdmin", Subject: ar}}}))
	} else {
		if account.Password != "" && !CheckPassword(password, account.Password) {
			return ChallengeResult{}, ErrUnauthorized
		}
		// Anonymous principal roles are merged only after successful email proof.
		if c.Context.Principal.ID != "" && !same(c.Context.Principal, principal) {
			var old PrincipalState
			oa, e := s.load(ctx, c.Context.Principal, &old)
			if e != nil {
				return ChallengeResult{}, e
			}
			if old.Subject.ID != "" {
				return ChallengeResult{}, ErrForbidden
			}
			var target PrincipalState
			ta, e := s.load(ctx, principal, &target)
			if e != nil {
				return ChallengeResult{}, e
			}
			if len(old.Roles) > 0 {
				events = append(events, s.event(principal, "add-roles", key+"/merge-roles", ta.Version, map[string]any{"roles": old.Roles}), s.event(c.Context.Principal, "remove-roles", key+"/remove-roles", oa.Version, map[string]any{"roles": old.Roles}))
			}
			groups, e := s.groupsForPrincipal(ctx, c.Context.Principal)
			if e != nil {
				return ChallengeResult{}, e
			}
			for _, g := range groups {
				var gs GroupState
				if e = json.Unmarshal(g.State, &gs); e != nil {
					return ChallengeResult{}, e
				}
				for _, p := range gs.Principals {
					if same(p, c.Context.Principal) {
						events = append(events, s.event(g.Root, "add-principals", key+"/group-add/"+g.Root.ID, g.Version+1, map[string]any{"principals": []Ref{principal}}), s.event(g.Root, "remove-principals", key+"/group-remove/"+g.Root.ID, g.Version+2, map[string]any{"principals": []Ref{c.Context.Principal}}))
						break
					}
				}
			}
		}
	}
	n, e := rand.Int(rand.Reader, big.NewInt(1000000))
	if e != nil {
		return ChallengeResult{}, e
	}
	code := fmt.Sprintf("%06d", n.Int64())
	hash, e := HashPassword(code)
	if e != nil {
		return ChallengeResult{}, e
	}
	mail := Mail{From: "authenticator@" + s.Network, To: email, Subject: "Auth code", Body: code + " is your verification code."}
	mb, e := json.Marshal(mail)
	if e != nil {
		return ChallengeResult{}, e
	}
	sealed, e := Seal(s.EncryptionKey, mb)
	if e != nil {
		return ChallengeResult{}, e
	}
	upgrade := c.Context
	upgrade.Principal = principal
	upgrade.Account = ar
	st := ChallengeState{RequestHash: requestHash, Code: hash, Expires: s.now().Add(3 * time.Minute).Unix(), Session: c.Context.Session, Upgrade: upgrade, Events: events, Delivery: base64.RawURLEncoding.EncodeToString(sealed)}
	if e = s.append(ctx, s.event(cr, "issue", key+"/issue", 0, st)); e != nil {
		return ChallengeResult{}, e
	}
	result, e := s.challengeResult(ctx, c, cr, st)
	if e != nil {
		return result, e
	}
	if s.Mail == nil {
		return result, ErrUnavailable
	}
	return result, s.Mail.Send(ctx, mail)
}

// IssueChallenge authenticates an existing account; registration uses SaveSession.
func (s *Service) IssueChallenge(ctx context.Context, token, key, email, password string) (ChallengeResult, error) {
	normalized, e := normalizeEmail(email)
	if e != nil {
		return ChallengeResult{}, e
	}
	id, e := s.emailID(normalized)
	if e != nil {
		return ChallengeResult{}, e
	}
	var a AccountState
	agg, e := s.load(ctx, s.root("core", "account", id), &a)
	if e != nil {
		return ChallengeResult{}, e
	}
	if agg.Version == 0 {
		return ChallengeResult{}, ErrUnauthorized
	}
	return s.SaveSession(ctx, token, key, normalized, password)
}
func (s *Service) challengeResult(ctx context.Context, c Claims, r Ref, st ChallengeState) (ChallengeResult, error) {
	if !same(st.Session, c.Context.Session) || st.Answered != "" || st.Expires <= s.now().Unix() {
		return ChallengeResult{}, ErrUnauthorized
	}
	c.ID = r.ID
	t, e := s.sign(ctx, c, "challenge", c.Context, st.Expires)
	return ChallengeResult{Challenge: r, Token: t, Expires: st.Expires}, e
}

// RetryChallengeDelivery is explicit at-least-once delivery; SMTP acceptance cannot prove receipt.
func (s *Service) RetryChallengeDelivery(ctx context.Context, token string) error {
	c, e := s.authenticate(ctx, token, "challenge")
	if e != nil {
		return e
	}
	var st ChallengeState
	_, e = s.load(ctx, s.root("core", "challenge", c.ID), &st)
	if e != nil {
		return e
	}
	if st.Answered != "" || st.Expires <= s.now().Unix() || !same(st.Session, c.Context.Session) {
		return ErrUnauthorized
	}
	sealed, e := base64.RawURLEncoding.DecodeString(st.Delivery)
	if e != nil {
		return e
	}
	raw, e := Open(s.EncryptionKey, sealed)
	if e != nil {
		return e
	}
	var mail Mail
	if e = json.Unmarshal(raw, &mail); e != nil {
		return e
	}
	if s.Mail == nil {
		return ErrUnavailable
	}
	return s.Mail.Send(ctx, mail)
}
func (s *Service) AnswerChallenge(ctx context.Context, token, key, code string) (SessionResult, error) {
	if e := validKey(key); e != nil {
		return SessionResult{}, e
	}
	c, e := s.authenticate(ctx, token, "challenge")
	if e != nil {
		return SessionResult{}, e
	}
	r := s.root("core", "challenge", c.ID)
	var st ChallengeState
	a, e := s.load(ctx, r, &st)
	if e != nil {
		return SessionResult{}, e
	}
	if a.Version == 0 || st.Expires <= s.now().Unix() || !same(st.Session, c.Context.Session) {
		return SessionResult{}, ErrUnauthorized
	}
	if len(s.HMACKey) < 32 {
		return SessionResult{}, ErrUnavailable
	}
	proof := hmac.New(sha256.New, s.HMACKey)
	proof.Write([]byte(r.Key() + "\x00" + code))
	proofHash := hex.EncodeToString(proof.Sum(nil))
	answerKey := stableID(r.Key(), key)
	if st.Answered != "" {
		// Even recovery must have a persisted budget: otherwise a consumed proof
		// becomes an unlimited code-guess oracle through the same request key.
		if st.RecoveryAttempts >= MaxChallengeAttempts {
			return SessionResult{}, ErrUnauthorized
		}
		if e = s.append(ctx, s.event(r, "recover", key+fmt.Sprintf("/recover/%d", st.RecoveryAttempts+1), a.Version, map[string]any{"attempts": st.RecoveryAttempts + 1, "hash": proofHash})); e != nil {
			return SessionResult{}, e
		}
		if st.AnswerKey != answerKey {
			return SessionResult{}, eventstore.ErrConflict
		}
		if !hmac.Equal([]byte(st.ProofHash), []byte(proofHash)) {
			return SessionResult{}, eventstore.ErrIdempotency
		}
	} else {
		// Reserve and bind the attempt before evaluating the guess. Reusing a request
		// key for another guess cannot bypass the persisted attempt budget.
		op, reserved, err := s.operation(ctx, r, "attempt", key+"/attempt")
		if err != nil {
			return SessionResult{}, err
		}
		if reserved {
			var prior struct {
				Attempts int    `json:"attempts"`
				Hash     string `json:"hash"`
			}
			if len(op.Inputs) != 1 || decode(op.Inputs[0].Payload, &prior) != nil || !hmac.Equal([]byte(prior.Hash), []byte(proofHash)) {
				return SessionResult{}, eventstore.ErrIdempotency
			}
		} else {
			if st.Attempts >= MaxChallengeAttempts {
				return SessionResult{}, ErrUnauthorized
			}
			if e = s.append(ctx, s.event(r, "attempt", key+"/attempt", a.Version, map[string]any{"attempts": st.Attempts + 1, "hash": proofHash})); e != nil {
				return SessionResult{}, e
			}
		}
		a, e = s.load(ctx, r, &st)
		if e != nil {
			return SessionResult{}, e
		}
		if st.Answered != "" {
			if st.AnswerKey != answerKey {
				return SessionResult{}, eventstore.ErrConflict
			}
			if !hmac.Equal([]byte(st.ProofHash), []byte(proofHash)) {
				return SessionResult{}, eventstore.ErrIdempotency
			}
		}
		if !CheckPassword(code, st.Code) {
			return SessionResult{}, ErrUnauthorized
		}
	}
	// A consumed challenge never performs its effects again. A matching proof can
	// recover the already-committed session after a lost response.
	var session SessionState
	sa, e := s.load(ctx, st.Session, &session)
	if e != nil {
		return SessionResult{}, e
	}
	if session.Terminated != "" {
		return SessionResult{}, ErrUnauthorized
	}
	if st.Answered == "" {
		ev := append([]eventstore.Input{}, st.Events...)
		ev = append(ev, s.event(r, "answer", key+"/answer", a.Version, map[string]string{"answered": s.now().Format(time.RFC3339Nano), "answerKey": answerKey, "proofHash": proofHash}), s.event(st.Session, "upgrade", key+"/upgrade", sa.Version, map[string]Ref{"principal": st.Upgrade.Principal, "account": st.Upgrade.Account}))
		if e = s.append(ctx, ev...); e != nil {
			if errors.Is(e, eventstore.ErrConflict) || errors.Is(e, eventstore.ErrIdempotency) || errors.Is(e, eventstore.ErrUncertain) {
				var committed ChallengeState
				if _, lookupErr := s.load(ctx, r, &committed); lookupErr == nil && committed.Answered != "" {
					if committed.AnswerKey != answerKey {
						return SessionResult{}, eventstore.ErrConflict
					}
					if hmac.Equal([]byte(committed.ProofHash), []byte(proofHash)) {
						return s.AnswerChallenge(ctx, token, key, code)
					}
				}
			}
			return SessionResult{}, e
		}
	} else if !same(session.Account, st.Upgrade.Account) || !same(session.Principal, st.Upgrade.Principal) {
		return SessionResult{}, errors.New("consumed challenge session differs")
	}
	upgraded := st.Upgrade
	upgraded.Scene = session.Scene
	upgraded.Device = session.Device
	upgraded.Subject = Ref{}
	if session.Scene.ID != "" {
		var scene SceneState
		if _, e = s.load(ctx, session.Scene, &scene); e != nil {
			return SessionResult{}, e
		}
		if scene.Deleted != "" {
			return SessionResult{}, ErrUnauthorized
		}
		upgraded.Subject = scene.Subject
	}
	t, e := s.sign(ctx, Claims{ID: stableID(r.ID, "access")}, "access", upgraded, session.Expires)
	return SessionResult{Context: upgraded, Token: t, Session: st.Session}, e
}
