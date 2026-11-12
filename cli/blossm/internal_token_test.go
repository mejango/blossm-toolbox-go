package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/lib/jwt"
	command "github.com/mejango/blossm-toolbox-go/procedures/command"
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
	c.Signing = &Signing{Provider: "local", Issuer: "issuer.example", Audience: "example.test", Subject: "configured-worker"}
	c.Secrets["JWT_PRIVATE_KEY"] = Secret{Reference: "sample-jwt-key", Version: "1"}
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
	e := Execution{Getenv: func(string) string { t.Fatal("offline read credentials"); return "" }, OpenJWT: func(context.Context, string, string) (*token.JWT[command.Context], func(), error) {
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
	return (token.LocalSigner{Key: k.key}).SignES256(ctx, message)
}
func TestInternalTokenKMSSelectionAndFailures(t *testing.T) {
	key := internalSigningKey(t)
	for _, provider := range []string{"gcp", "aws"} {
		for _, failure := range []string{"", "open", "sign", "mismatch"} {
			c := internalExample()
			c.Signing.Provider, c.Signing.KMSKey = provider, "key-ref"
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
			}, OpenJWT: func(got context.Context, issuer, audience string) (*token.JWT[command.Context], func(), error) {
				opened++
				if got != ctx || issuer != c.Signing.Issuer || audience != c.Signing.Audience {
					t.Fatal("lost context/network")
				}
				cleanup := func() { closed++ }
				if failure == "open" {
					return nil, cleanup, errors.New("secret provider detail")
				}
				return &token.JWT[command.Context]{Signer: token.KMSSigner{Client: kms, Key: c.Signing.KMSKey}, VerifyKey: &key.PublicKey, Issuer: issuer, Audience: audience}, cleanup, nil
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
	if err := RunWith(ctx, []string{"internal-token", "--config", internalConfig(t, internalExample()), "--execute"}, &out, &out, Execution{OpenJWT: func(context.Context, string, string) (*token.JWT[command.Context], func(), error) {
		t.Fatal("opened cancelled signer")
		return nil, nil, nil
	}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
