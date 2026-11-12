package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"blossm.local/backend/command"
	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/transport"
)

type peerState struct {
	mu      sync.Mutex
	root    eventstore.Root
	session dia.SessionState
}

func (s *peerState) Aggregate(ctx context.Context, root eventstore.Root) (eventstore.Aggregate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return eventstore.Aggregate{}, err
	}
	if root != s.root {
		return eventstore.Aggregate{Root: root, Version: -1}, nil
	}
	raw, err := json.Marshal(s.session)
	return eventstore.Aggregate{Root: root, Version: 0, State: raw}, err
}
func (*peerState) Append(context.Context, []eventstore.Input) ([]eventstore.Receipt, error) {
	return nil, errors.New("unexpected fixture mutation")
}
func (*peerState) Query(context.Context, string, string, string, []eventstore.Predicate) ([]eventstore.Aggregate, error) {
	return nil, errors.New("unexpected fixture query")
}

func TestConfiguredDelegationOverHTTP(t *testing.T) {
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwt := dia.JWT{Signer: dia.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.n", Audience: "n"}
	session := eventstore.Root{Network: "n", Service: "core", Domain: "session", ID: "session"}
	principal := eventstore.Root{Network: "n", Service: "core", Domain: "principal", ID: "principal"}
	now := time.Now()
	state := &peerState{root: session, session: dia.SessionState{Started: now.Unix(), Expires: now.Add(time.Hour).Unix(), Principal: principal}}
	identity := &dia.Service{Store: state, JWT: &jwt, Network: "n"}
	receiver, err := configurePeers(config{Network: "n", Service: "destination", DelegatedSubject: "n/source"}, jwt, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.close()
	captured := make(chan command.Request, 1)
	authenticate := transport.Authenticator{Bearer: receiver.delegated}
	framework := transport.Framework{Authorize: func(r *http.Request) (transport.Identity, error) {
		verified, e := authenticate.Authenticate(r)
		if e != nil {
			return verified, e
		}
		return verified, transport.DelegatedCommandCheck(r.Context(), verified, transport.GatewayRequest{Request: r})
	}, Commands: map[string]command.Procedure{"set": {Main: func(_ context.Context, in command.Request) (command.Result, error) {
		captured <- in
		return command.Result{Response: command.Contribution{StatusCode: 204}}, nil
	}}}}
	handler, err := framework.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	sender, err := configurePeers(config{Network: "n", Service: "source", OutboundURL: server.URL, OutboundService: "destination"}, jwt, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.close()
	if sender.outbound.Target != "n/destination" || sender.delegated != nil {
		t.Fatal("wrong configured peer direction")
	}
	claims := dia.Claims{Issuer: jwt.Issuer, Audience: []string{"n"}, Subject: principal.ID, ID: "access", Kind: "access", NotBefore: now.Unix(), Expires: state.session.Expires, Context: dia.SessionContext{Network: "n", Session: session, Principal: principal}}
	encodedContext, err := eventstore.Encode(claims.Context)
	if err != nil {
		t.Fatal(err)
	}
	var values command.Context
	if err = eventstore.Decode(encodedContext, &values); err != nil {
		t.Fatal(err)
	}
	rawClaims, err := eventstore.Encode(claims)
	if err != nil {
		t.Fatal(err)
	}
	issued := now.UTC().Format(time.RFC3339Nano)
	parent := command.Request{ID: "parent", Issued: issued, Context: values, Claims: rawClaims, Transaction: eventstore.Transaction{ID: "parent-transaction", Issued: issued, Path: []eventstore.Trace{{Network: "n", Host: "source", Procedure: "command", Hash: "v1"}}}}
	if _, err = sender.outbound.Execute(ctx, parent, "set", "copy-document", command.Request{}); err != nil {
		t.Fatal(err)
	}
	received := <-captured
	var delegated dia.Claims
	if err = eventstore.Decode(received.Claims, &delegated); err != nil {
		t.Fatal(err)
	}
	if delegated.Subject != "n/source" || len(delegated.Audience) != 1 || delegated.Audience[0] != "n/destination" || delegated.Context != claims.Context || delegated.Expires > claims.Expires || received.Transaction.ID != parent.Transaction.ID {
		t.Fatal("delegation changed authority or trace", delegated)
	}
	if jwt.Audience != "n" {
		t.Fatal("runtime mutated user JWT verifier")
	}
	state.mu.Lock()
	state.session.Terminated = "terminated"
	state.mu.Unlock()
	if _, err = sender.outbound.Execute(ctx, parent, "set", "copy-document", command.Request{}); !errors.Is(err, dia.ErrUnauthorized) {
		t.Fatal("stale user dispatched", err)
	}
	select {
	case <-captured:
		t.Fatal("stale user reached destination")
	default:
	}
	none, err := configurePeers(config{Network: "n", Service: "source"}, jwt, identity)
	if err != nil || none.outbound != nil || none.delegated != nil {
		t.Fatal("unconfigured peer route enabled", err)
	}
	none.close()
}

func TestPeerConfigurationFailsClosed(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"missing target": {"OUTBOUND_URL": "https://target.test"}, "missing URL": {"OUTBOUND_SERVICE": "target"},
		"plaintext remote":      {"OUTBOUND_URL": "http://target.test", "OUTBOUND_SERVICE": "target"},
		"URL credentials":       {"OUTBOUND_URL": "https://user:password@target.test", "OUTBOUND_SERVICE": "target"},
		"ambiguous target":      {"OUTBOUND_URL": "https://target.test", "OUTBOUND_SERVICE": "target/other"},
		"foreign incoming peer": {"DELEGATED_SUBJECT": "other/peer"}, "ambiguous incoming peer": {"DELEGATED_SUBJECT": "fixture/peer/other"},
	} {
		t.Run(name, func(t *testing.T) {
			fixtureEnv(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			if _, err := loadConfig(); err == nil {
				t.Fatal("unsafe peers accepted")
			}
		})
	}
	t.Run("configured", func(t *testing.T) {
		fixtureEnv(t)
		t.Setenv("OUTBOUND_URL", "https://target.test")
		t.Setenv("OUTBOUND_SERVICE", "target")
		t.Setenv("DELEGATED_SUBJECT", "fixture/peer")
		if _, err := loadConfig(); err != nil {
			t.Fatal(err)
		}
	})
}
