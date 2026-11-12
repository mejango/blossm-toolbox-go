package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/projection"
	"blossm.local/backend/runtimeauth"
	"blossm.local/backend/serviceapp"
	"blossm.local/backend/transport"
)

func internalConfig(t *testing.T, c Config) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "config.json")
	data, err := encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	return file
}
func internalExample() Config {
	c := Example("gcp", "postgres")
	c.Runtime = "framework"
	c.Service = "documents"
	c.InternalSubject = "configured-worker"
	c.IdentityNamespace = "identity"
	c.Delivery = deliveryExample("gcp")
	return c
}
func internalSigningKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SIGNER", "local")
	t.Setenv("JWT_KMS_KEY", "")
	t.Setenv("JWT_PUBLIC_KEY", "")
	t.Setenv("JWT_PRIVATE_KEY", string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw})))
	return key
}

func TestInternalTokenOfflineAndBounds(t *testing.T) {
	file := internalConfig(t, internalExample())
	e := Execution{Getenv: func(string) string { t.Fatal("offline read credentials"); return "" }, OpenJWT: func(context.Context, string) (*dia.JWT, func(), error) {
		t.Fatal("offline opened signer")
		return nil, nil, nil
	}}
	var out bytes.Buffer
	if err := RunWith(context.Background(), []string{"internal-token", "--config", file}, &out, &out, e); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"status": "prepared-only"`) || !strings.Contains(out.String(), `"subject": "configured-worker"`) {
		t.Fatal(out.String())
	}
	for _, extra := range [][]string{{"--ttl", "0s"}, {"--ttl", "6m"}, {"--execute", "--dry-run"}} {
		if err := RunWith(context.Background(), append([]string{"internal-token", "--config", file}, extra...), &out, &out, e); err == nil {
			t.Fatal("invalid issuer invocation accepted")
		}
	}
}

type tokenRoots struct {
	eventstore.Reader
	root eventstore.Root
}

func (r tokenRoots) Roots(ctx context.Context, yield func(eventstore.Root) error) error {
	return yield(r.root)
}
func (r tokenRoots) Count(context.Context, eventstore.Root) (int64, error) { return 1, nil }

type tokenBackend struct {
	eventstore.Backend
	root eventstore.Root
}

func (b *tokenBackend) View(ctx context.Context, fn func(eventstore.Reader) error) error {
	return fn(tokenRoots{root: b.root})
}

type tokenReplay struct{ requests []projection.Request }

func (p *tokenReplay) Enqueue(_ context.Context, r projection.Request) error {
	p.requests = append(p.requests, r)
	return nil
}

func TestInternalTokenDrivesServiceAppReplay(t *testing.T) {
	internalSigningKey(t)
	c := internalExample()
	ctx := context.Background()
	jwt, close, err := runtimeauth.Open(ctx, c.Network)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	verifier, err := transport.ServiceJWTVerifier(*jwt, c.InternalSubject)
	if err != nil {
		t.Fatal(err)
	}
	root := eventstore.Root{Network: c.Network, Service: c.Service, Domain: serviceapp.Domain, ID: "doc"}
	backend := &tokenBackend{root: root}
	identityBackend := &tokenBackend{root: root}
	producer := &tokenReplay{}
	identity := &dia.Service{Network: c.Network, JWT: jwt, Store: &eventstore.Store{Backend: identityBackend, Local: true}}
	app, err := serviceapp.New(ctx, serviceapp.Config{Store: &eventstore.Store{Backend: backend, Local: true}, Identity: identity, Service: c.Service, Internal: verifier, Replay: producer})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler)
	defer server.Close()
	input := filepath.Join(t.TempDir(), "replay.json")
	if err = os.WriteFile(input, []byte(`{"runId":"cli-run","limit":10}`), 0600); err != nil {
		t.Fatal(err)
	}
	config := internalConfig(t, c)
	for _, kind := range []string{"valid", "wrong-subject", "wrong-audience"} {
		t.Run(kind, func(t *testing.T) {
			minted := c
			if kind == "wrong-subject" {
				minted.InternalSubject = "other-worker"
			}
			if kind == "wrong-audience" {
				minted.Network = "other.test"
			}
			var token, stderr bytes.Buffer
			if err := Run(ctx, []string{"internal-token", "--config", internalConfig(t, minted), "--ttl", "2m", "--execute"}, &token, &stderr); err != nil {
				t.Fatal(err)
			}
			value := strings.TrimSpace(token.String())
			if kind == "valid" {
				claims, err := jwt.Verify(value, "service")
				if err != nil || claims.Subject != c.InternalSubject || len(claims.Audience) != 1 || claims.Audience[0] != c.Network || claims.Expires-claims.NotBefore != 120 || claims.ID == "" {
					t.Fatal(claims, err)
				}
			}
			before := len(producer.requests)
			var output bytes.Buffer
			e := Execution{HTTP: server.Client(), Getenv: func(string) string { return value }}
			err := RunWith(ctx, []string{"replay", "--config", config, "--url", server.URL + "/projections/documents/replay", "--input", input, "--idempotency-key", "cli-replay", "--execute"}, &output, &stderr, e)
			if kind == "valid" {
				var ack transport.ProjectionReplayOutput
				if err != nil || json.Unmarshal(output.Bytes(), &ack) != nil || ack.RunID != "cli-run" || ack.Enqueued != 1 || !ack.Complete || len(producer.requests) != before+1 || producer.requests[before].RunID != "cli-run" || producer.requests[before].Root != root {
					t.Fatal("minted replay failed", ack, err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "401") || len(producer.requests) != before {
					t.Fatal("foreign authority accepted", err)
				}
			}
		})
	}
}

type tokenKMS struct {
	key   *ecdsa.PrivateKey
	calls int
	fail  bool
}

func (k *tokenKMS) SignES256(ctx context.Context, key string, message []byte) ([]byte, error) {
	k.calls++
	if key != "key-ref" {
		return nil, errors.New("wrong key")
	}
	if k.fail {
		return nil, errors.New("secret provider detail")
	}
	return (dia.LocalSigner{Key: k.key}).SignES256(ctx, message)
}
func TestInternalTokenKMSSelectionAndFailures(t *testing.T) {
	key := internalSigningKey(t)
	for _, provider := range []string{"gcp", "aws"} {
		for _, failure := range []string{"", "open", "sign", "mismatch"} {
			c := internalExample()
			c.Signing = Signing{Provider: provider, KMSKey: "key-ref"}
			c.Secrets["JWT_PUBLIC_KEY"] = c.Secrets["JWT_PRIVATE_KEY"]
			delete(c.Secrets, "JWT_PRIVATE_KEY")
			opened, closed := 0, 0
			kms := &tokenKMS{key: key, fail: failure == "sign"}
			ctx := context.WithValue(context.Background(), struct{}{}, "signing-context")
			e := Execution{Getenv: func(name string) string {
				if name == "JWT_SIGNER" {
					if failure == "mismatch" {
						return "local"
					}
					return provider
				}
				if name == "JWT_KMS_KEY" {
					return "key-ref"
				}
				t.Fatal(name)
				return ""
			}, OpenJWT: func(got context.Context, network string) (*dia.JWT, func(), error) {
				opened++
				if got != ctx || network != c.Network {
					t.Fatal("lost context/network")
				}
				cleanup := func() { closed++ }
				if failure == "open" {
					return nil, cleanup, errors.New("secret provider detail")
				}
				return &dia.JWT{Signer: dia.KMSSigner{Client: kms, Key: c.Signing.KMSKey}, VerifyKey: &key.PublicKey, Issuer: "dia." + network, Audience: network}, cleanup, nil
			}}
			var out bytes.Buffer
			err := RunWith(ctx, []string{"internal-token", "--config", internalConfig(t, c), "--execute"}, &out, &out, e)
			if failure == "" {
				if err != nil || kms.calls != 1 || opened != 1 || closed != 1 {
					t.Fatal(err, kms.calls, opened, closed)
				}
			} else {
				if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "secret provider detail") {
					t.Fatal("failure leaked output", err, out.String())
				}
				if failure == "mismatch" {
					if opened != 0 {
						t.Fatal("opened mismatched signer")
					}
				} else if opened != 1 || closed != 1 {
					t.Fatal("cleanup", opened, closed)
				}
			}
		}
	}
	// A cancelled operation must never initialize its signer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := RunWith(ctx, []string{"internal-token", "--config", internalConfig(t, internalExample()), "--execute"}, &out, &out, Execution{OpenJWT: func(context.Context, string) (*dia.JWT, func(), error) {
		t.Fatal("opened cancelled signer")
		return nil, nil, nil
	}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
