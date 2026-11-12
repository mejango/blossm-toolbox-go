//go:build integration

package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	mongoStore "blossm.local/backend/storage/mongo"
	"blossm.local/backend/storage/postgres"
)

type recoveryProvider struct {
	mu    sync.Mutex
	fail  bool
	calls int
	mail  dia.Mail
}

func (p *recoveryProvider) Send(_ context.Context, m dia.Mail) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.mail = m
	if p.fail {
		return dia.ErrUnavailable
	}
	return nil
}

type recoverySMS struct{ *recoveryProvider }

func (p recoverySMS) Send(_ context.Context, _, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return dia.ErrUnavailable
	}
	return nil
}
func (p *recoveryProvider) setFailure(v bool) { p.mu.Lock(); defer p.mu.Unlock(); p.fail = v }

func TestNativeHTTPRecovery(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			namespace := fmt.Sprintf("http_recovery_%d", time.Now().UnixNano())
			var backend eventstore.Backend
			var err error
			if kind == "postgres" {
				if os.Getenv("POSTGRES_URL") == "" {
					t.Fatal("POSTGRES_URL required")
				}
				backend, err = postgres.Open(ctx, os.Getenv("POSTGRES_URL"), namespace)
			} else {
				if os.Getenv("MONGODB_URI") == "" {
					t.Fatal("MONGODB_URI required")
				}
				backend, err = mongoStore.Open(ctx, mongoStore.Config{URI: os.Getenv("MONGODB_URI"), Database: "blossm_test", Namespace: namespace})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close(ctx)
			store := &eventstore.Store{Backend: backend, Reducers: dia.Reducers()}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			provider := &recoveryProvider{fail: true}
			app := &dia.Service{Store: store, Network: "local", JWT: &dia.JWT{Signer: dia.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.local", Audience: "local"}, HMACKey: bytes.Repeat([]byte("h"), 32), EncryptionKey: bytes.Repeat([]byte("e"), 32), Mail: provider, SMS: recoverySMS{provider}}
			server := httptest.NewServer(API{Dia: app, Store: store}.Handler())
			defer server.Close()
			request := func(path, token, key string, input any, status int) json.RawMessage {
				t.Helper()
				body, e := json.Marshal(input)
				if e != nil {
					t.Fatal(e)
				}
				req, e := http.NewRequest("POST", server.URL+path, bytes.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Idempotency-Key", key)
				response, e := server.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer response.Body.Close()
				var out json.RawMessage
				if e = json.NewDecoder(response.Body).Decode(&out); e != nil {
					t.Fatal(e)
				}
				if response.StatusCode != status {
					t.Fatalf("%s: status %d want %d: %s", path, response.StatusCode, status, out)
				}
				return out
			}
			secret, _ := dia.NewSessionSecret()
			var session dia.SessionResult
			json.Unmarshal(request("/commands/session/start", "", secret, map[string]any{"device": dia.Device{Type: "desktop", Platform: "browser", Agent: "fixture"}}, 201), &session)
			var partial struct {
				Error  string              `json:"error"`
				Result dia.ChallengeResult `json:"result"`
			}
			failed := request("/commands/session/save", session.Token, "save", map[string]string{"email": "person@example.com", "password": ""}, 503)
			if err = json.Unmarshal(failed, &partial); err != nil || partial.Result.Token == "" || partial.Result.Challenge.ID == "" || partial.Error == "" || bytes.Contains(failed, []byte("committed")) {
				t.Fatalf("missing truthful challenge recovery: %s", failed)
			}
			provider.setFailure(false)
			request("/commands/challenge/retry-delivery", partial.Result.Token, "retry", struct{}{}, 200)
			provider.mu.Lock()
			code := strings.Fields(provider.mail.Body)[0]
			provider.mu.Unlock()
			json.Unmarshal(request("/commands/challenge/answer", partial.Result.Token, "answer", map[string]string{"code": code}, 200), &session)
			claims, err := app.AuthenticateAccess(ctx, session.Token)
			if err != nil {
				t.Fatal(err)
			}
			subject := claims.Context.Principal
			if err = app.CreateRole(ctx, session.Token, "role", "CommsSender", []string{"comms:email:send", "comms:sms:send"}); err != nil {
				t.Fatal(err)
			}
			// Bootstrap the fixture's provider authority through the actual reducer/store.
			aggregate, err := store.Aggregate(ctx, subject)
			if err != nil {
				t.Fatal(err)
			}
			expected := aggregate.Version + 1
			payload, _ := json.Marshal(map[string]any{"roles": []dia.RoleGrant{{ID: "CommsSender", Subject: subject}}})
			stamp := time.Now().UTC().Format(time.RFC3339Nano)
			_, err = store.Append(ctx, []eventstore.Input{{Root: subject, Action: "add-roles", Key: "fixture-authority", Expected: &expected, Created: stamp, Payload: payload, Transaction: eventstore.Transaction{ID: "fixture-authority", Issued: stamp}}})
			if err != nil {
				t.Fatal(err)
			}
			for _, domain := range []string{"email", "sms"} {
				provider.setFailure(true)
				input := map[string]any{"subject": subject}
				if domain == "email" {
					input["message"] = dia.Mail{From: "sender@example.com", To: "recipient@example.com", Subject: "hello", Body: "private body"}
				} else {
					input["to"] = "+15551234567"
					input["message"] = "private body"
				}
				path := "/commands/" + domain + "/send"
				var envelope struct {
					Error  string  `json:"error"`
					Result dia.Ref `json:"result"`
				}
				failed = request(path, session.Token, domain, input, 503)
				json.Unmarshal(failed, &envelope)
				if envelope.Result.ID == "" || envelope.Error == "" || bytes.Contains(failed, []byte("committed")) {
					t.Fatalf("missing ref: %s", failed)
				}
				before, e := store.Aggregate(ctx, envelope.Result)
				if e != nil || before.Version != 0 || bytes.Contains(before.State, []byte("private body")) {
					t.Fatalf("intent not durable/encrypted: %v %s", e, before.State)
				}
				provider.setFailure(false)
				var repeated dia.Ref
				json.Unmarshal(request(path, session.Token, domain, input, 200), &repeated)
				after, e := store.Aggregate(ctx, repeated)
				if e != nil || repeated != envelope.Result || after.Version != before.Version {
					t.Fatal("retry changed intent", e)
				}
				if domain == "email" {
					input["message"] = dia.Mail{From: "sender@example.com", To: "recipient@example.com", Subject: "changed", Body: "private body"}
				} else {
					input["message"] = "changed"
				}
				request(path, session.Token, domain, input, 409)
				unauthorized := request(path, "", domain, input, 401)
				if bytes.Contains(unauthorized, []byte(`"result"`)) {
					t.Fatal("unauthorized result leak")
				}
			}
		})
	}
}
