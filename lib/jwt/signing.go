package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"

	"github.com/mejango/blossm-toolbox-go/lib/authentication_error"
)

// ES256Signer signs the original message, hashing it exactly once with SHA-256,
// and returns the 64-byte JOSE R||S signature. Implementations honor ctx.
type ES256Signer interface {
	SignES256(context.Context, []byte) ([]byte, error)
}

// LocalSigner retains private key ownership outside the JWT verifier.
type LocalSigner struct{ Key *ecdsa.PrivateKey }

func (s LocalSigner) SignES256(ctx context.Context, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key := s.Key
	if key == nil || !validPublicKey(&key.PublicKey) || key.D == nil || key.D.Sign() <= 0 || key.D.Cmp(elliptic.P256().Params().N) >= 0 {
		return nil, errToken
	}
	x, y := elliptic.P256().ScalarBaseMult(key.D.Bytes())
	if x.Cmp(key.X) != 0 || y.Cmp(key.Y) != 0 {
		return nil, errToken
	}
	digest := sha256.Sum256(message)
	r, v, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		return nil, err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	v.FillBytes(signature[32:])
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return signature, nil
}

// KMSClient is satisfied by cloud/gcp.KMS and cloud/aws.KMS. Their existing
// adapters own provider key validation, hashing and DER-to-JOSE conversion.
type KMSClient interface {
	SignES256(context.Context, string, []byte) ([]byte, error)
}

type KMSSigner struct {
	Client KMSClient
	Key    string
}

func (s KMSSigner) SignES256(ctx context.Context, message []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Client == nil || s.Key == "" {
		return nil, authn.ErrUnavailable
	}
	return s.Client.SignES256(ctx, s.Key, message)
}

func validPublicKey(key *ecdsa.PublicKey) bool {
	return key != nil && key.Curve == elliptic.P256() && key.X != nil && key.Y != nil && key.Curve.IsOnCurve(key.X, key.Y)
}
