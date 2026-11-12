package dia

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/eventstore"
)

// This consumer fake uses the real reducers and raw last-event versions. It
// stages the complete batch, including operation receipts, before committing.
type systemTestStore struct {
	mu         sync.Mutex
	states     map[Ref]eventstore.Aggregate
	batches    [][]eventstore.Input
	failBefore error
	failAfter  error
}

func newSystemTestStore() *systemTestStore {
	return &systemTestStore{states: map[Ref]eventstore.Aggregate{}}
}
func (m *systemTestStore) seed(r Ref, state any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := json.Marshal(state)
	if err != nil {
		panic(err)
	}
	m.states[r] = eventstore.Aggregate{Root: r, Version: 0, State: b}
}
func (m *systemTestStore) snapshot(r Ref) eventstore.Aggregate {
	a, _ := m.Aggregate(context.Background(), r)
	return a
}
func (m *systemTestStore) Aggregate(ctx context.Context, r Ref) (eventstore.Aggregate, error) {
	if err := ctx.Err(); err != nil {
		return eventstore.Aggregate{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.states[r]
	if !ok {
		return eventstore.Aggregate{Root: r, Version: -1, State: json.RawMessage(`{}`)}, nil
	}
	a.State = append(json.RawMessage(nil), a.State...)
	return a, nil
}
func (m *systemTestStore) Query(ctx context.Context, network, service, domain string, predicates []eventstore.Predicate) ([]eventstore.Aggregate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []eventstore.Aggregate{}
	for root, a := range m.states {
		if root.Network != network || root.Service != service || root.Domain != domain {
			continue
		}
		ok, err := eventstore.Matches(a.State, predicates)
		if err != nil {
			return nil, err
		}
		if ok {
			a.State = append(json.RawMessage(nil), a.State...)
			out = append(out, a)
		}
	}
	return out, nil
}
func (m *systemTestStore) Append(ctx context.Context, inputs []eventstore.Input) ([]eventstore.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failBefore != nil {
		return nil, m.failBefore
	}
	next := make(map[Ref]eventstore.Aggregate, len(m.states))
	for r, a := range m.states {
		next[r] = a
	}
	receipts := make([]eventstore.Receipt, 0, len(inputs))
	reducers := Reducers()
	for _, in := range inputs {
		a, ok := next[in.Root]
		if !ok {
			a = eventstore.Aggregate{Root: in.Root, Version: -1, State: json.RawMessage(`{}`)}
		}
		if in.Expected != nil && *in.Expected != a.Version+1 {
			return nil, eventstore.ErrConflict
		}
		reducer, ok := reducers[in.Root.Service+"/"+in.Root.Domain]
		if !ok {
			return nil, ErrInvalid
		}
		event := eventstore.Event{Input: in, Number: a.Version + 1}
		state, err := reducer.Apply(a.State, event)
		if err != nil {
			return nil, err
		}
		a.State = state
		a.Version++
		next[in.Root] = a
		receipts = append(receipts, eventstore.Receipt{Outcome: eventstore.Committed, Event: event})
	}
	m.states = next
	m.batches = append(m.batches, append([]eventstore.Input(nil), inputs...))
	if m.failAfter != nil {
		err := m.failAfter
		m.failAfter = nil
		return nil, err
	}
	return receipts, nil
}

func systemTestService(t *testing.T) (*Service, *systemTestStore, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Unix(1800000000, 0) }
	store := newSystemTestStore()
	s := &Service{Store: store, Network: "test.network", HMACKey: []byte(strings.Repeat("h", 32)), Now: now, JWT: &JWT{Signer: LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.test", Audience: "test.network", Now: now}}
	principal := s.root("core", "principal", "test-principal")
	session := s.root("core", "session", "test-session")
	store.seed(principal, PrincipalState{})
	store.seed(session, SessionState{Principal: principal, Started: now().Unix() - 1, Expires: now().Add(time.Hour).Unix()})
	sc := SessionContext{Network: s.Network, Principal: principal, Session: session}
	token, err := s.sign(context.Background(), Claims{ID: "test-token"}, "access", sc, now().Add(time.Hour).Unix())
	if err != nil {
		t.Fatal(err)
	}
	return s, store, token
}
func systemNodeAndKey(t *testing.T) (*Service, *systemTestStore, NodeResult, KeyResult) {
	t.Helper()
	s, store, token := systemTestService(t)
	node, err := s.RegisterNode(context.Background(), token, "register-node", NodeInput{Network: "node.network"})
	if err != nil {
		t.Fatal(err)
	}
	key, err := s.RegisterKey(context.Background(), node.Token, "access", "register-key", KeyInput{Node: node.Node, Name: "worker", Roles: []string{"NodeNetwork"}})
	if err != nil {
		t.Fatal(err)
	}
	return s, store, node, key
}

func TestSystemNodeAtomicRegistrationAndReplay(t *testing.T) {
	ctx := context.Background()
	s, store, token := systemTestService(t)
	store.failBefore = errors.New("database unavailable")
	if _, err := s.RegisterNode(ctx, token, "node", NodeInput{Network: "node.network"}); err == nil {
		t.Fatal("failure swallowed")
	}
	if len(store.states) != 2 {
		t.Fatal("partial registration")
	}
	store.failBefore = nil
	node, err := s.RegisterNode(ctx, token, "node", NodeInput{Network: "node.network"})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.batches) != 1 {
		t.Fatal("node registration must be one atomic batch")
	}
	var principal PrincipalState
	if err = json.Unmarshal(store.snapshot(node.Principal).State, &principal); err != nil {
		t.Fatal(err)
	}
	if principal.Subject != (Ref{}) {
		t.Fatal("node bootstrap blocks subsequent account subject binding")
	}
	var group GroupState
	if err = json.Unmarshal(store.snapshot(node.Group).State, &group); err != nil {
		t.Fatal(err)
	}
	if len(group.Principals) != 1 || group.Principals[0] != node.Principal || len(group.Networks) != 1 || group.Networks[0] != "node.network" {
		t.Fatalf("group: %+v", group)
	}
	replay, err := s.RegisterNode(ctx, node.Token, "node", NodeInput{Network: "node.network"})
	if err != nil || replay.Node != node.Node || len(store.batches) != 1 {
		t.Fatalf("replay: %+v, %v", replay, err)
	}
	if _, err = s.RegisterNode(ctx, node.Token, "node", NodeInput{Network: "other.network"}); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatalf("changed replay: %v", err)
	}
	recovered, err := s.RegisterNode(ctx, token, "node", NodeInput{Network: "node.network"})
	if err != nil || recovered.Node != node.Node || len(store.batches) != 1 {
		t.Fatalf("original token recovery: %v", err)
	}
	if _, err = s.authenticate(ctx, recovered.Token, "access"); err != nil {
		t.Fatal(err)
	}
}

func TestSystemKeySecretRecoveryAndAuthorization(t *testing.T) {
	ctx := context.Background()
	s, store, node, key := systemNodeAndKey(t)
	if strings.Contains(string(store.snapshot(key.Key).State), key.Secret) {
		t.Fatal("plaintext key persisted")
	}
	var principal PrincipalState
	if err := json.Unmarshal(store.snapshot(key.Principal).State, &principal); err != nil {
		t.Fatal(err)
	}
	if principal.Subject != key.Key {
		t.Fatal("key principal has missing or wrong subject root")
	}
	read, err := s.ReadKey(ctx, node.Token, "access", key.Key)
	if err != nil || read.Secret != "" {
		t.Fatalf("public key view leaked verifier: %v", err)
	}
	count := len(store.batches)
	recovered, err := s.RegisterKey(ctx, node.Token, "access", "register-key", KeyInput{Node: node.Node, Name: "worker", Roles: []string{"NodeNetwork", "NodeNetwork"}})
	if err != nil || recovered != key || len(store.batches) != count {
		t.Fatalf("recovery: %v", err)
	}
	if _, err = s.RegisterKey(ctx, node.Token, "access", "register-key", KeyInput{Node: node.Node, Name: "changed", Roles: []string{"NodeNetwork"}}); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatalf("changed key: %v", err)
	}
	if _, err = s.RegisterKey(ctx, node.Token, "access", "admin-key", KeyInput{Node: node.Node, Name: "bad", Roles: []string{"NodeAdmin"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("privilege escalation: %v", err)
	}
	other := node.Node
	other.ID = "another-node"
	if _, err = s.RegisterKey(ctx, node.Token, "access", "foreign", KeyInput{Node: other, Name: "bad", Roles: []string{"NodeNetwork"}}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cross-subject grant: %v", err)
	}
	store.failAfter = eventstore.ErrUncertain
	input := KeyInput{Node: node.Node, Name: "uncertain", Roles: []string{"NodeNetwork"}}
	if _, err = s.RegisterKey(ctx, node.Token, "access", "uncertain", input); err != nil {
		t.Fatalf("lost response: %v", err)
	}
	count = len(store.batches)
	recovered, err = s.RegisterKey(ctx, node.Token, "access", "uncertain", input)
	if err != nil || recovered.Secret == "" || len(store.batches) != count {
		t.Fatalf("uncertain recovery: %v", err)
	}
	s.HMACKey = []byte("short")
	if _, err = s.RegisterKey(ctx, node.Token, "access", "short", input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("weak HMAC accepted: %v", err)
	}
}

func TestSystemConnectionUpdatesAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, _, node, key := systemNodeAndKey(t)
	if _, err := s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: strings.Repeat("0", 64)}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad secret: %v", err)
	}
	connection, err := s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret})
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.JWT.Verify(connection.Token, "connection")
	if err != nil || c.Subject != key.Principal.ID || c.Context.Subject != key.Key {
		t.Fatalf("connection claims: %+v %v", c, err)
	}
	updates, err := s.IssueUpdatesToken(ctx, connection.Token, "connection")
	if err != nil {
		t.Fatal(err)
	}
	uc, err := s.JWT.Verify(updates, "connection-updates")
	if err != nil || uc.Expires > s.now().Add(time.Hour).Unix() || uc.Expires > c.Expires {
		t.Fatalf("update expiry: %v", err)
	}
	channel, err := s.UpdatesChannel(ctx, updates, "connection-updates", ChannelInput{Name: "sessions"})
	if err != nil || channel == "" {
		t.Fatalf("channel: %v", err)
	}
	if _, err = s.UpdatesChannel(ctx, updates, "updates", ChannelInput{Name: "sessions"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("kind confusion: %v", err)
	}
	if _, err = s.IssueUpdatesToken(ctx, updates, "connection-updates"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("updates token escalated: %v", err)
	}
	if err = s.PublishUpdate(ctx, connection.Token, "connection", ChannelInput{Name: "sessions"}, Update{ID: "1", Type: "update"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing publisher: %v", err)
	}
	pub := &systemTestPublisher{}
	s.Updates = pub
	if err = s.PublishUpdate(ctx, connection.Token, "connection", ChannelInput{Name: "sessions"}, Update{ID: "1", Type: "update"}); err != nil || pub.channel != channel {
		t.Fatalf("publish channel mismatch: %v", err)
	}
	if err = s.RevokeKey(ctx, node.Token, "access", "revoke", key.Key); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked key accepted: %v", err)
	}
	if _, err = s.IssueUpdatesToken(ctx, connection.Token, "connection"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked connection accepted: %v", err)
	}
	if _, err = s.UpdatesChannel(ctx, updates, "connection-updates", ChannelInput{Name: "sessions"}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked subscription accepted: %v", err)
	}
}

type systemTestPublisher struct {
	channel string
	err     error
}

func (p *systemTestPublisher) Publish(_ context.Context, channel string, _ Update) error {
	p.channel = channel
	return p.err
}

type systemTestMailer struct {
	calls int
	err   error
}

func (m *systemTestMailer) Send(context.Context, Mail) error { m.calls++; return m.err }

type systemTestSMS struct {
	calls int
	err   error
}

func (m *systemTestSMS) Send(context.Context, string, string) error { m.calls++; return m.err }

func TestSystemCommsExplicitAuthorityAndProviderFailures(t *testing.T) {
	ctx := context.Background()
	s, store, token := systemTestService(t)
	c, err := s.JWT.Verify(token, "access")
	if err != nil {
		t.Fatal(err)
	}
	subject := s.root("system", "node", "comms-node")
	message := Mail{From: "sender@example.com", To: "recipient@example.com", Subject: "hello", Body: "message"}
	if err = s.SendMail(ctx, token, "access", subject, message); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized mail: %v", err)
	}
	store.seed(s.root("core", "role", "CommsSender"), RoleState{ID: "CommsSender", Network: s.Network, Principal: c.Context.Principal, Subject: subject, Permissions: []string{"comms:email:send", "comms:sms:send"}})
	store.seed(c.Context.Principal, PrincipalState{Roles: []RoleGrant{{ID: "CommsSender", Subject: subject}}})
	if err = s.SendMail(ctx, token, "access", subject, message); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing mail adapter: %v", err)
	}
	if err = s.SendSMS(ctx, token, "access", subject, "+15551234567", "hello"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing SMS adapter: %v", err)
	}
	failure := errors.New("provider rejected request")
	mailer := &systemTestMailer{err: failure}
	sms := &systemTestSMS{err: failure}
	s.Mail = mailer
	s.SMS = sms
	if err = s.SendMail(ctx, token, "access", subject, message); !errors.Is(err, failure) {
		t.Fatalf("mail failure swallowed: %v", err)
	}
	if err = s.SendSMS(ctx, token, "access", subject, "+15551234567", "hello"); !errors.Is(err, failure) {
		t.Fatalf("sms failure swallowed: %v", err)
	}
	message.Subject = "injection\r\nBcc: victim@example.com"
	if err = s.SendMail(ctx, token, "access", subject, message); !errors.Is(err, ErrInvalid) || mailer.calls != 1 {
		t.Fatalf("header injection reached adapter: %v", err)
	}
	if err = s.SendSMS(ctx, token, "access", subject, "not-a-number", "hello"); !errors.Is(err, ErrInvalid) || sms.calls != 1 {
		t.Fatalf("bad phone reached adapter: %v", err)
	}
}

func TestSystemConnectionBindingAndKeyRotation(t *testing.T) {
	ctx := context.Background()
	s, store, node, key := systemNodeAndKey(t)
	connection, err := s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret})
	if err != nil {
		t.Fatal(err)
	}
	s.HMACKey = []byte(strings.Repeat("r", 32))
	if _, err = s.RegisterKey(ctx, node.Token, "access", "register-key", KeyInput{Node: node.Node, Name: "worker", Roles: []string{"NodeNetwork"}}); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatalf("rotation silently returned a different secret: %v", err)
	}
	if _, err = s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret}); err != nil {
		t.Fatalf("rotation invalidated stored verifier: %v", err)
	}
	foreign := key.Key
	foreign.Network = "foreign.network"
	if _, err = s.OpenConnection(ctx, ConnectionInput{Key: foreign, Secret: key.Secret}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("foreign key: %v", err)
	}
	var principal PrincipalState
	if err = json.Unmarshal(store.snapshot(key.Principal).State, &principal); err != nil {
		t.Fatal(err)
	}
	principal.Subject = s.root("system", "key", "different-key")
	store.seed(key.Principal, principal)
	if _, err = s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("misbound principal: %v", err)
	}
	if _, err = s.IssueUpdatesToken(ctx, connection.Token, "connection"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("existing connection bypassed principal binding: %v", err)
	}
}

func TestSystemAnonymousNodeAndExpiredParentToken(t *testing.T) {
	ctx := context.Background()
	s, store, token := systemTestService(t)
	c, err := s.JWT.Verify(token, "access")
	if err != nil {
		t.Fatal(err)
	}
	var session SessionState
	if err = json.Unmarshal(store.snapshot(c.Context.Session).State, &session); err != nil {
		t.Fatal(err)
	}
	session.Principal = Ref{}
	store.seed(c.Context.Session, session)
	c.Context.Principal = Ref{}
	token, err = s.sign(ctx, c, "access", c.Context, c.Expires)
	if err != nil {
		t.Fatal(err)
	}
	node, err := s.RegisterNode(ctx, token, "anonymous-node", NodeInput{Network: "node.network"})
	if err != nil {
		t.Fatal(err)
	}
	var principal PrincipalState
	if err = json.Unmarshal(store.snapshot(node.Principal).State, &principal); err != nil {
		t.Fatal(err)
	}
	if principal.Subject != (Ref{}) || len(principal.Roles) != 3 {
		t.Fatalf("anonymous principal %+v", principal)
	}
	key, err := s.RegisterKey(ctx, node.Token, "access", "anon-key", KeyInput{Node: node.Node, Name: "anonymous", Roles: []string{"NodeNetwork"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := s.JWT.Verify(connection.Token, "connection")
	if err != nil {
		t.Fatal(err)
	}
	claims.Expires = s.now().Add(30 * time.Second).Unix()
	short, err := s.JWT.Sign(ctx, claims)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := s.IssueUpdatesToken(ctx, short, "connection")
	if err != nil {
		t.Fatal(err)
	}
	uc, err := s.JWT.Verify(updates, "connection-updates")
	if err != nil || uc.Expires != claims.Expires {
		t.Fatalf("updates outlive parent: %+v, %v", uc, err)
	}
	expired := s.now().Add(time.Minute)
	s.Now = func() time.Time { return expired }
	s.JWT.Now = s.Now
	if _, err = s.IssueUpdatesToken(ctx, short, "connection"); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expired connection accepted: %v", err)
	}
}

func TestSystemCommsCommandsPersistEncryptedIntentBeforeDelivery(t *testing.T) {
	ctx := context.Background()
	s, store, token := systemTestService(t)
	s.EncryptionKey = []byte(strings.Repeat("e", 32))
	c, err := s.JWT.Verify(token, "access")
	if err != nil {
		t.Fatal(err)
	}
	subject := s.root("system", "node", "comms-node")
	store.seed(s.root("core", "role", "CommsSender"), RoleState{ID: "CommsSender", Network: s.Network, Principal: c.Context.Principal, Subject: subject, Permissions: []string{"comms:email:send", "comms:sms:send"}})
	store.seed(c.Context.Principal, PrincipalState{Roles: []RoleGrant{{ID: "CommsSender", Subject: subject}}})
	message := Mail{From: "sender@example.com", To: "private@example.com", Subject: "private subject", Body: "private authentication code"}
	mailer := &systemTestMailer{}
	s.Mail = mailer
	store.failBefore = errors.New("storage offline")
	if _, err = s.SendMailCommand(ctx, token, "access", "mail", subject, message); err == nil || mailer.calls != 0 {
		t.Fatal("delivery before durable intent")
	}
	store.failBefore = nil
	store.failAfter = eventstore.ErrUncertain
	root, err := s.SendMailCommand(ctx, token, "access", "mail", subject, message)
	if err != nil || mailer.calls != 1 || root.ID == "" {
		t.Fatalf("visible preparation receipt: %v", err)
	}
	raw := string(store.snapshot(root).State)
	for _, secret := range []string{message.To, message.Subject, message.Body} {
		if strings.Contains(raw, secret) {
			t.Fatalf("plaintext in comms aggregate: %s", secret)
		}
	}
	for _, batch := range store.batches {
		encoded, _ := json.Marshal(batch)
		if strings.Contains(string(encoded), message.To) || strings.Contains(string(encoded), message.Body) {
			t.Fatal("plaintext in operation receipt")
		}
	}
	before := len(store.batches)
	repeated, err := s.SendMailCommand(ctx, token, "access", "mail", subject, message)
	if err != nil || root != repeated || mailer.calls != 2 || len(store.batches) != before {
		t.Fatalf("prepared retry: %v", err)
	}
	if _, err = s.SendMailCommand(ctx, token, "access", "mail", subject, message); err != nil || mailer.calls != 3 {
		t.Fatalf("explicit resend: %v", err)
	}
	message.Body = "changed message"
	if _, err = s.SendMailCommand(ctx, token, "access", "mail", subject, message); !errors.Is(err, eventstore.ErrIdempotency) || mailer.calls != 3 {
		t.Fatalf("changed delivery intent: %v", err)
	}
	sms := &systemTestSMS{err: errors.New("provider failed")}
	s.SMS = sms
	smsRoot, err := s.SendSMSCommand(ctx, token, "access", "sms", subject, "+15551234567", "private code")
	if !errors.Is(err, sms.err) || smsRoot.ID == "" || sms.calls != 1 {
		t.Fatalf("sms provider failure: %v", err)
	}
	if strings.Contains(string(store.snapshot(smsRoot).State), "15551234567") {
		t.Fatal("plaintext phone persisted")
	}
	s.SMS = nil
	if _, err = s.SendSMSCommand(ctx, token, "access", "sms", subject, "+15551234567", "private code"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing adapter stub: %v", err)
	}
}

func TestSystemNodeLostResponseRecoveryIsExact(t *testing.T) {
	for _, anonymous := range []bool{true, false} {
		name := "account"
		if anonymous {
			name = "anonymous"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, store, token := systemTestService(t)
			claims, err := s.JWT.Verify(token, "access")
			if err != nil {
				t.Fatal(err)
			}
			var session SessionState
			if err = decode(store.snapshot(claims.Context.Session).State, &session); err != nil {
				t.Fatal(err)
			}
			if anonymous {
				session.Principal = Ref{}
				claims.Context.Principal = Ref{}
			} else {
				session.Account = s.root("core", "account", "original-account")
				claims.Context.Account = session.Account
				session.Scene = s.root("core", "scene", "original-scene")
				claims.Context.Scene = session.Scene
				claims.Context.Subject = session.Account
				store.seed(session.Scene, SceneState{Subject: session.Account, Group: s.root("core", "group", "original-group")})
			}
			store.seed(claims.Context.Session, session)
			token, err = s.sign(ctx, claims, "access", claims.Context, claims.Expires)
			if err != nil {
				t.Fatal(err)
			}
			store.failAfter = eventstore.ErrUncertain
			input := NodeInput{Network: "node.network"}
			if _, err = s.RegisterNode(ctx, token, "lost-node", input); err != nil {
				t.Fatalf("lost response: %v", err)
			}
			if _, err = s.authenticate(ctx, token, "access"); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("original token should be stale: %v", err)
			}
			count := len(store.batches)
			recovered, err := s.RegisterNode(ctx, token, "lost-node", input)
			if err != nil {
				t.Fatal(err)
			}
			current, err := s.authenticate(ctx, recovered.Token, "access")
			if err != nil {
				t.Fatal(err)
			}
			if current.Context.Principal != recovered.Principal || current.Context.Scene != recovered.Scene || current.Context.Account != claims.Context.Account || len(store.batches) != count {
				t.Fatal("recovery changed identity or appended events")
			}
			if _, err = s.RegisterNode(ctx, token, "uncommitted-node", input); !errors.Is(err, ErrUnauthorized) || len(store.batches) != count {
				t.Fatalf("stale token created new node: %v", err)
			}
			if _, err = s.RegisterNode(ctx, token, "lost-node", NodeInput{Network: "different.network"}); !errors.Is(err, eventstore.ErrIdempotency) {
				t.Fatalf("changed request recovered: %v", err)
			}
			var committed SessionState
			if err = decode(store.snapshot(claims.Context.Session).State, &committed); err != nil {
				t.Fatal(err)
			}
			for _, change := range []string{"account", "principal", "scene", "terminated"} {
				later := committed
				switch change {
				case "account":
					later.Account = s.root("core", "account", "later-account")
				case "principal":
					later.Principal = s.root("core", "principal", "later-principal")
				case "scene":
					later.Scene = s.root("core", "scene", "later-scene")
				case "terminated":
					later.Terminated = s.now().Format(time.RFC3339)
				}
				store.seed(claims.Context.Session, later)
				if _, err = s.RegisterNode(ctx, token, "lost-node", input); !errors.Is(err, ErrUnauthorized) || len(store.batches) != count {
					t.Fatalf("recovered later %s transition: %v", change, err)
				}
			}
			store.seed(claims.Context.Session, committed)
			store.seed(recovered.Principal, PrincipalState{})
			if _, err = s.RegisterNode(ctx, token, "lost-node", input); !errors.Is(err, ErrForbidden) {
				t.Fatalf("recovery bypassed removed NodeAdmin: %v", err)
			}
		})
	}
}
