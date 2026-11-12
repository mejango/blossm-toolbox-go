package dia

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"strings"
	"testing"
	"time"
)

type captureMailer struct {
	messages []Mail
	err      error
}

func (m *captureMailer) Send(ctx context.Context, message Mail) error {
	m.messages = append(m.messages, message)
	return m.err
}
func coreService(t *testing.T) (*Service, *systemTestStore, *captureMailer) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	store := newSystemTestStore()
	mail := &captureMailer{}
	now := func() time.Time { return time.Unix(1900000000, 0) }
	s := &Service{Store: store, Network: "dia.test", JWT: &JWT{Signer: LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.test", Audience: "dia.test", Now: now}, Now: now, EncryptionKey: []byte(strings.Repeat("e", 32)), HMACKey: []byte(strings.Repeat("h", 32)), Mail: mail}
	return s, store, mail
}
func startCore(t *testing.T, s *Service) (SessionResult, string) {
	t.Helper()
	secret, e := NewSessionSecret()
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.StartSession(context.Background(), secret, Device{Type: "desktop", Platform: "browser", Agent: "tests"})
	if e != nil {
		t.Fatal(e)
	}
	return r, secret
}
func TestSessionLifecycleAndCreationCredential(t *testing.T) {
	ctx := context.Background()
	s, _, _ := coreService(t)
	if _, e := s.StartSession(ctx, "predictable-request-id", Device{}); !errors.Is(e, ErrInvalid) {
		t.Fatal(e)
	}
	first, secret := startCore(t, s)
	again, e := s.StartSession(ctx, secret, first.Context.Device)
	if e != nil || again.Session != first.Session {
		t.Fatal(again, e)
	}
	if _, e = s.AuthenticateAccess(ctx, first.Token); e != nil {
		t.Fatal(e)
	}
	if e = s.TerminateSession(ctx, first.Token, "terminate"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, first.Token); !errors.Is(e, ErrUnauthorized) {
		t.Fatal(e)
	}
	if _, e = s.StartSession(ctx, secret, first.Context.Device); !errors.Is(e, ErrUnauthorized) {
		t.Fatal(e)
	}
}
func TestAccountChallengeAtomicUpgradeAndReplay(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	first, secret := startCore(t, s)
	challenge, e := s.SaveSession(ctx, first.Token, "save", " PERSON@Example.com ", "correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	if len(mail.messages) != 1 {
		t.Fatal("mail not sent")
	}
	id, _ := s.emailID("person@example.com")
	if store.snapshot(s.root("core", "account", id)).Version != -1 {
		t.Fatal("account persisted before proof")
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "wrong", "000000x"); !errors.Is(e, ErrUnauthorized) {
		t.Fatal(e)
	}
	code := strings.Fields(mail.messages[0].Body)[0]
	result, e := s.AnswerChallenge(ctx, challenge.Token, "answer", code)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, result.Token); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, first.Token); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("stale token accepted", e)
	}
	if _, e = s.StartSession(ctx, secret, first.Context.Device); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("creation secret recovered upgraded identity", e)
	}
	before := store.snapshot(result.Context.Account).Version
	recovered, e := s.AnswerChallenge(ctx, challenge.Token, "answer", code)
	if e != nil || recovered.Context.Account != result.Context.Account {
		t.Fatal(recovered, e)
	}
	if store.snapshot(result.Context.Account).Version != before {
		t.Fatal("proof replay repeated account effects")
	}
	if e = s.SetPassword(ctx, result.Token, "set", "another long password"); e == nil {
		t.Fatal("existing password replaced")
	}
}
func TestChallengeProviderFailureAndCommitFailure(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	first, _ := startCore(t, s)
	mail.err = errors.New("provider down")
	challenge, e := s.SaveSession(ctx, first.Token, "save", "person@example.com", "")
	if e == nil || challenge.Token == "" {
		t.Fatal("missing durable challenge on delivery failure", e)
	}
	mail.err = nil
	if e = s.RetryChallengeDelivery(ctx, challenge.Token); e != nil {
		t.Fatal(e)
	}
	code := strings.Fields(mail.messages[0].Body)[0]
	store.failBefore = errors.New("database down")
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "answer", code); e == nil {
		t.Fatal("expected commit error")
	}
	id, _ := s.emailID("person@example.com")
	if store.snapshot(s.root("core", "account", id)).Version != -1 {
		t.Fatal("partial account write")
	}
	store.failBefore = nil
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "answer", code); e != nil {
		t.Fatal(e)
	}
}
func TestSceneGroupAndRoleAuthority(t *testing.T) {
	ctx := context.Background()
	s, _, _ := coreService(t)
	first, _ := startCore(t, s)
	subject := s.root("system", "node", "example")
	scene, e := s.RegisterScene(ctx, first.Token, "scene", subject)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, first.Token); !errors.Is(e, ErrUnauthorized) {
		t.Fatal(e)
	}
	switched, e := s.ChangeScene(ctx, scene.Token, "switch", scene.Scene)
	if e != nil {
		t.Fatal(e)
	}
	if switched.Context.Subject != subject {
		t.Fatal("subject lost")
	}
	stranger, _ := startCore(t, s)
	if e = s.DeleteScene(ctx, stranger.Token, "delete", scene.Scene); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	if e = s.CreateRole(ctx, switched.Token, "role", "CustomReader", []string{"core:scene:read"}); e != nil {
		t.Fatal(e)
	}
	if e = s.ChangeRoles(ctx, switched.Token, "grant", scene.Principal, []RoleGrant{{ID: "CustomReader", Subject: scene.Scene}}, false); e != nil {
		t.Fatal(e)
	}
	if e = s.ChangeRolePermissions(ctx, switched.Token, "expand", "CustomReader", []string{"core:scene:delete"}, false); e == nil {
		t.Fatal("granted role expanded")
	}
	if e = s.DeleteScene(ctx, switched.Token, "delete", scene.Scene); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, switched.Token); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("deleted scene accepted", e)
	}
}
func TestAtomicReceiptRejectsIntentDrift(t *testing.T) {
	ctx := context.Background()
	s, store, _ := coreService(t)
	r := s.root("core", "group", "g")
	ev := s.event(r, "add-networks", "same", 0, map[string]any{"networks": []string{s.Network}})
	if e := s.append(ctx, ev); e != nil {
		t.Fatal(e)
	}
	ev.Expected = new(int64)
	*ev.Expected = 1
	ev.Created = time.Now().Format(time.RFC3339Nano)
	if e := s.append(ctx, ev); e != nil {
		t.Fatal(e)
	}
	if store.snapshot(r).Version != 0 {
		t.Fatal("duplicate event")
	}
	changed := s.event(r, "add-networks", "same", 1, map[string]any{"networks": []string{"other.test"}})
	if e := s.append(ctx, changed); !errors.Is(e, eventstore.ErrIdempotency) {
		t.Fatal(e)
	}
}

func TestChallengeAttemptLimitAndIntentBinding(t *testing.T) {
	ctx := context.Background()
	s, _, mail := coreService(t)
	start, _ := startCore(t, s)
	challenge, e := s.SaveSession(ctx, start.Token, "save", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.SaveSession(ctx, start.Token, "save", "other@example.com", ""); !errors.Is(e, eventstore.ErrIdempotency) {
		t.Fatal("changed intent accepted", e)
	}
	for i := 0; i < 5; i++ {
		if _, e = s.AnswerChallenge(ctx, challenge.Token, fmt.Sprintf("wrong-%d", i), "not-the-code"); !errors.Is(e, ErrUnauthorized) {
			t.Fatal(e)
		}
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "correct", strings.Fields(mail.messages[0].Body)[0]); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("exhausted challenge accepted", e)
	}
}

func TestAnonymousAccountMergeMovesRolesAndGroupsAfterProof(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	first, _ := startCore(t, s)
	challenge, e := s.SaveSession(ctx, first.Token, "save-original", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	owner, e := s.AnswerChallenge(ctx, challenge.Token, "answer-original", strings.Fields(mail.messages[0].Body)[0])
	if e != nil {
		t.Fatal(e)
	}
	second, _ := startCore(t, s)
	scene, e := s.RegisterScene(ctx, second.Token, "anon-scene", s.root("example", "document", "document"))
	if e != nil {
		t.Fatal(e)
	}
	challenge, e = s.SaveSession(ctx, scene.Token, "merge", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	var before GroupState
	if e = decode(store.snapshot(scene.Group).State, &before); e != nil {
		t.Fatal(e)
	}
	if len(before.Principals) != 1 || before.Principals[0] != scene.Principal {
		t.Fatal("membership changed before proof")
	}
	merged, e := s.AnswerChallenge(ctx, challenge.Token, "answer-merge", strings.Fields(mail.messages[len(mail.messages)-1].Body)[0])
	if e != nil {
		t.Fatal(e)
	}
	if merged.Context.Principal != owner.Context.Principal {
		t.Fatal("anonymous principal retained")
	}
	var after GroupState
	if e = decode(store.snapshot(scene.Group).State, &after); e != nil {
		t.Fatal(e)
	}
	if len(after.Principals) != 1 || after.Principals[0] != owner.Context.Principal {
		t.Fatalf("membership not moved: %+v", after)
	}
	if _, e = s.ChangeScene(ctx, merged.Token, "enter-merged", scene.Scene); e != nil {
		t.Fatal(e)
	}
}

func TestSceneUpgradeLostResponseRecoveryIsBounded(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	start, _ := startCore(t, s)
	subject := s.root("example", "document", "d")
	store.failAfter = eventstore.ErrUncertain
	if _, e := s.RegisterScene(ctx, start.Token, "scene", subject); e != nil {
		t.Fatal(e)
	}
	recovered, e := s.RegisterScene(ctx, start.Token, "scene", subject)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, recovered.Token); e != nil {
		t.Fatal(e)
	}
	challenge, e := s.SaveSession(ctx, recovered.Token, "save", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "answer", strings.Fields(mail.messages[0].Body)[0]); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RegisterScene(ctx, start.Token, "scene", subject); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("old anonymous operation recovered later account identity", e)
	}
}

func TestConcurrentChallengeAnswerCommitsOnce(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	start, _ := startCore(t, s)
	challenge, e := s.SaveSession(ctx, start.Token, "save", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	code := strings.Fields(mail.messages[0].Body)[0]
	errorsCh := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, e := s.AnswerChallenge(ctx, challenge.Token, fmt.Sprintf("answer-%d", i), code)
			errorsCh <- e
		}(i)
	}
	success := 0
	for i := 0; i < 2; i++ {
		e := <-errorsCh
		if e == nil {
			success++
		} else if !errors.Is(e, eventstore.ErrConflict) {
			t.Fatal(e)
		}
	}
	if success < 1 {
		t.Fatal("no answer committed")
	}
	id, _ := s.emailID("person@example.com")
	if a := store.snapshot(s.root("core", "account", id)); a.Version != 0 {
		t.Fatalf("account created more than once: %+v", a)
	}
}

func TestNormalizeContracts(t *testing.T) {
	for _, in := range []string{"919 555 1144", "(919) 555-1144", "+19195551144"} {
		got, e := NormalizePhoneNumber(in)
		if e != nil || got != "+19195551144" {
			t.Fatal(got, e)
		}
	}
	for _, in := range []string{"919", "+0123456789", "+1\n9195551144"} {
		if _, e := NormalizePhoneNumber(in); e == nil {
			t.Fatal("invalid phone accepted", in)
		}
	}
	if _, e := NormalizeEmail("a..b@example.com"); e == nil {
		t.Fatal("invalid email accepted")
	}
}

func TestChallengeSameKeyCannotProbeMultipleCodes(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	start, _ := startCore(t, s)
	challenge, e := s.SaveSession(ctx, start.Token, "save", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "guess", "wrong"); !errors.Is(e, ErrUnauthorized) {
		t.Fatal(e)
	}
	actual := strings.Fields(mail.messages[0].Body)[0]
	for _, guess := range []string{"another", actual} {
		if _, e = s.AnswerChallenge(ctx, challenge.Token, "guess", guess); !errors.Is(e, eventstore.ErrIdempotency) {
			t.Fatal("request key allowed another guess", e)
		}
	}
	var state ChallengeState
	if e = decode(store.snapshot(challenge.Challenge).State, &state); e != nil || state.Attempts != 1 {
		t.Fatal(state, e)
	}
	result, e := s.AnswerChallenge(ctx, challenge.Token, "correct", actual)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, result.Token); e != nil {
		t.Fatal(e)
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "after-consumption", actual); !errors.Is(e, eventstore.ErrConflict) {
		t.Fatal("consumed challenge accepted new request", e)
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "correct", actual); e != nil {
		t.Fatal("same operation recovery failed", e)
	}
}

func TestGroupCreationRetryAndRemovalKeepRolesAligned(t *testing.T) {
	ctx := context.Background()
	s, store, _ := coreService(t)
	start, _ := startCore(t, s)
	scene, e := s.RegisterScene(ctx, start.Token, "scene", s.root("example", "document", "d"))
	if e != nil {
		t.Fatal(e)
	}
	friend := s.root("core", "principal", "friend")
	group, e := s.AddGroupPrincipals(ctx, scene.Token, "group", Ref{}, []GroupPrincipal{{Principal: friend, Roles: []string{"GroupMember"}}})
	if e != nil {
		t.Fatal(e)
	}
	before := store.snapshot(group).Version
	if again, e := s.AddGroupPrincipals(ctx, scene.Token, "group", Ref{}, []GroupPrincipal{{Principal: friend, Roles: []string{"GroupMember"}}}); e != nil || again != group {
		t.Fatal(again, e)
	}
	if store.snapshot(group).Version != before {
		t.Fatal("creation repeated events")
	}
	if e = s.RemovePrincipals(ctx, scene.Token, "remove", group, []Ref{friend}); e != nil {
		t.Fatal(e)
	}
	if e = s.RemovePrincipals(ctx, scene.Token, "remove", group, []Ref{friend}); e != nil {
		t.Fatal("removal retry", e)
	}
	var state PrincipalState
	if e = decode(store.snapshot(friend).State, &state); e != nil || len(state.Roles) != 0 {
		t.Fatal(state, e)
	}
}

func TestConcurrentSameAnswerKeyRecoversExactCommit(t *testing.T) {
	ctx := context.Background()
	s, store, mail := coreService(t)
	s.Now = time.Now
	s.JWT.Now = time.Now
	start, _ := startCore(t, s)
	challenge, e := s.SaveSession(ctx, start.Token, "save", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	code := strings.Fields(mail.messages[0].Body)[0]
	gate := make(chan struct{})
	out := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { <-gate; _, err := s.AnswerChallenge(ctx, challenge.Token, "same-answer", code); out <- err }()
	}
	close(gate)
	for i := 0; i < 2; i++ {
		if e := <-out; e != nil {
			t.Fatal(e)
		}
	}
	var proof ChallengeState
	if e = decode(store.snapshot(challenge.Challenge).State, &proof); e != nil || proof.Attempts != 1 || proof.Answered == "" {
		t.Fatal(proof, e)
	}
}

func TestConsumedChallengeRecoveryCannotBecomeGuessOracle(t *testing.T) {
	ctx := context.Background()
	s, _, mail := coreService(t)
	start, _ := startCore(t, s)
	challenge, e := s.SaveSession(ctx, start.Token, "save", "person@example.com", "")
	if e != nil {
		t.Fatal(e)
	}
	code := strings.Fields(mail.messages[0].Body)[0]
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "answer", code); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < MaxChallengeAttempts; i++ {
		if _, e = s.AnswerChallenge(ctx, challenge.Token, "answer", fmt.Sprintf("wrong-%d", i)); !errors.Is(e, eventstore.ErrIdempotency) {
			t.Fatal(e)
		}
	}
	if _, e = s.AnswerChallenge(ctx, challenge.Token, "answer", code); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("unbounded consumed proof oracle", e)
	}
}

func TestChangeSceneLostResponseRecoversOnlyCommittedTarget(t *testing.T) {
	ctx := context.Background()
	s, store, _ := coreService(t)
	start, _ := startCore(t, s)
	scene, e := s.RegisterScene(ctx, start.Token, "scene", s.root("example", "document", "d"))
	if e != nil {
		t.Fatal(e)
	}
	store.failAfter = eventstore.ErrUncertain
	if _, e = s.ChangeScene(ctx, scene.Token, "switch", scene.Scene); e != nil {
		t.Fatal(e)
	}
	before := store.snapshot(start.Session).Version
	recovered, e := s.ChangeScene(ctx, scene.Token, "switch", scene.Scene)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, recovered.Token); e != nil {
		t.Fatal(e)
	}
	if store.snapshot(start.Session).Version != before {
		t.Fatal("scene transition repeated")
	}
	if _, e = s.ChangeScene(ctx, scene.Token, "switch", s.root("core", "scene", "other")); !errors.Is(e, eventstore.ErrIdempotency) {
		t.Fatal("recovered unrelated target", e)
	}
}
