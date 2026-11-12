package runtimeauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/security/token"
)

type runtimeSigningClient struct {
	key         *ecdsa.PrivateKey
	wantContext context.Context
	calls       int
	t           *testing.T
}

func (c *runtimeSigningClient) SignES256(ctx context.Context, key string, message []byte) ([]byte, error) {
	c.calls++
	if ctx != c.wantContext || key != "configured-key" {
		c.t.Fatal("runtime binding lost")
	}
	return (token.LocalSigner{Key: c.key}).SignES256(ctx, message)
}
func signingKeys(t *testing.T) (*ecdsa.PrivateKey, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private})), string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: public}))
}
func cleanSigningEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"JWT_SIGNER", "JWT_PRIVATE_KEY", "JWT_KMS_KEY", "JWT_PUBLIC_KEY"} {
		t.Setenv(name, "")
	}
}

func TestRuntimeSigningSelection(t *testing.T) {
	key, private, public := signingKeys(t)
	for _, provider := range []string{"local", "gcp", "aws"} {
		t.Run(provider, func(t *testing.T) {
			cleanSigningEnv(t)
			t.Setenv("JWT_SIGNER", provider)
			if provider == "local" {
				t.Setenv("JWT_PRIVATE_KEY", private)
			} else {
				t.Setenv("JWT_KMS_KEY", "configured-key")
				t.Setenv("JWT_PUBLIC_KEY", public)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			client := &runtimeSigningClient{key: key, wantContext: ctx, t: t}
			opened, closed := 0, 0
			jwt, close, err := configuredJWT[struct{}](ctx, "token.network", "network", func(got context.Context, kind string) (token.KMSClient, func(), error) {
				if got != ctx || kind != provider {
					t.Fatal("wrong provider context")
				}
				opened++
				return client, func() { closed++ }, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if client.calls != 0 {
				t.Fatal("startup signed remotely")
			}
			claims := token.Claims[struct{}]{Issuer: "token.network", Audience: []string{"network"}, Subject: "subject", ID: "request", Kind: "access", NotBefore: time.Now().Unix(), Expires: time.Now().Add(time.Hour).Unix()}
			token, err := jwt.Sign(ctx, claims)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = jwt.Verify(token, "access"); err != nil {
				t.Fatal(err)
			}
			exported, err := jwt.PublicKey()
			if err != nil || string(exported) != public {
				t.Fatal("pinned public key changed", err)
			}
			close()
			if provider == "local" {
				if opened != 0 || closed != 0 || client.calls != 0 {
					t.Fatal("local used cloud")
				}
			} else if opened != 1 || closed != 1 || client.calls != 1 {
				t.Fatal("remote lifecycle mismatch", opened, closed, client.calls)
			}
		})
	}
}

func TestRuntimeSigningRejectsInvalidConfiguration(t *testing.T) {
	_, private, public := signingKeys(t)
	for name, env := range map[string]map[string]string{
		"missing selector": {}, "unknown selector": {"JWT_SIGNER": "other"},
		"missing local key":   {"JWT_SIGNER": "local"},
		"local mixed key":     {"JWT_SIGNER": "local", "JWT_PRIVATE_KEY": private, "JWT_PUBLIC_KEY": public},
		"local trailing PEM":  {"JWT_SIGNER": "local", "JWT_PRIVATE_KEY": private + private},
		"local wrong PEM":     {"JWT_SIGNER": "local", "JWT_PRIVATE_KEY": public},
		"cloud missing pin":   {"JWT_SIGNER": "gcp", "JWT_KMS_KEY": "configured-key"},
		"cloud missing key":   {"JWT_SIGNER": "aws", "JWT_PUBLIC_KEY": public},
		"cloud malformed pin": {"JWT_SIGNER": "aws", "JWT_KMS_KEY": "configured-key", "JWT_PUBLIC_KEY": "bad"},
		"cloud private pin":   {"JWT_SIGNER": "gcp", "JWT_KMS_KEY": "configured-key", "JWT_PUBLIC_KEY": private},
		"cloud mixed private": {"JWT_SIGNER": "aws", "JWT_KMS_KEY": "configured-key", "JWT_PUBLIC_KEY": public, "JWT_PRIVATE_KEY": private},
	} {
		t.Run(name, func(t *testing.T) {
			cleanSigningEnv(t)
			for key, value := range env {
				t.Setenv(key, value)
			}
			_, _, err := configuredJWT[struct{}](context.Background(), "token.network", "network", func(context.Context, string) (token.KMSClient, func(), error) {
				t.Fatal("invalid config constructed client")
				return nil, nil, nil
			})
			if err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	t.Run("factory error", func(t *testing.T) {
		cleanSigningEnv(t)
		t.Setenv("JWT_SIGNER", "gcp")
		t.Setenv("JWT_KMS_KEY", "configured-key")
		t.Setenv("JWT_PUBLIC_KEY", public)
		failure := errors.New("credentials unavailable")
		_, _, err := configuredJWT[struct{}](context.Background(), "token.network", "network", func(context.Context, string) (token.KMSClient, func(), error) { return nil, nil, failure })
		if !errors.Is(err, failure) {
			t.Fatal(err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := configuredJWT[struct{}](ctx, "token.network", "network", func(context.Context, string) (token.KMSClient, func(), error) {
			t.Fatal("canceled config reached provider")
			return nil, nil, nil
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	})
}
