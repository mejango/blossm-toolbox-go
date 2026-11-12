package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"
)

type signingFunc func(context.Context, []byte) ([]byte, error)

func (f signingFunc) SignES256(ctx context.Context, message []byte) ([]byte, error) {
	return f(ctx, message)
}

func TestJWTSigningBoundary(t *testing.T) {
	jwt, claims := testJWT(t)
	local := jwt.Signer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jwt.Signer = signingFunc(func(got context.Context, message []byte) ([]byte, error) {
		if got != ctx {
			t.Fatal("context lost")
		}
		signature, err := local.SignES256(got, message)
		cancel()
		return signature, err
	})
	if token, err := jwt.Sign(ctx, claims); !errors.Is(err, context.Canceled) || token != "" {
		t.Fatal("signed after cancellation", err)
	}
	for _, signature := range [][]byte{nil, make([]byte, 63), make([]byte, 64), make([]byte, 65)} {
		jwt.Signer = signingFunc(func(context.Context, []byte) ([]byte, error) { return signature, nil })
		if token, err := jwt.Sign(context.Background(), claims); err == nil || token != "" {
			t.Fatal("unverified signature issued")
		}
	}
	jwt.Signer = signingFunc(func(context.Context, []byte) ([]byte, error) {
		t.Fatal("invalid input reached signer")
		return nil, nil
	})
	claims.Subject = strings.Repeat("x", 16384)
	if _, err := jwt.Sign(context.Background(), claims); err == nil {
		t.Fatal("oversized token accepted")
	}
}

func TestJWTDoesNotIssueExpiredDuringSigning(t *testing.T) {
	jwt, claims := testJWT(t)
	local := jwt.Signer
	now := int64(100)
	jwt.Now = func() time.Time { return time.Unix(now, 0) }
	jwt.Signer = signingFunc(func(ctx context.Context, message []byte) ([]byte, error) {
		signature, err := local.SignES256(ctx, message)
		now = claims.Expires
		return signature, err
	})
	if token, err := jwt.Sign(context.Background(), claims); err == nil || token != "" {
		t.Fatal("issued token expired during provider call")
	}
}

func TestLocalSignerRejectsMalformedPrivateKeys(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	wrongCurve, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*ecdsa.PrivateKey{nil, {}, {PublicKey: key.PublicKey}, {PublicKey: other.PublicKey, D: key.D}, wrongCurve} {
		if _, err := (LocalSigner{Key: bad}).SignES256(context.Background(), []byte("message")); err == nil {
			t.Fatal("malformed signing key accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (LocalSigner{Key: key}).SignES256(ctx, []byte("message")); !errors.Is(err, context.Canceled) {
		t.Fatal("local signer ignored cancellation", err)
	}
}
