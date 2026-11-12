package aws

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"math/big"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type ecdsaSignature struct{ R, S *big.Int }

// ES256PublicKey parses the KMS DER SubjectPublicKeyInfo and rejects other curves
// or usages. There is no alias-keyed cache that can outlive a key rotation.
func (a KMS) ES256PublicKey(ctx context.Context, key string) (*ecdsa.PublicKey, error) {
	if key == "" {
		return nil, errors.New("aws kms: key required")
	}
	out, err := a.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: sdk.String(key)})
	if err != nil {
		return nil, err
	}
	if out == nil || out.KeySpec != types.KeySpecEccNistP256 || out.KeyUsage != types.KeyUsageTypeSignVerify {
		return nil, errors.New("aws kms: ES256 requires ECC_NIST_P256 SIGN_VERIFY")
	}
	parsed, err := x509.ParsePKIXPublicKey(out.PublicKey)
	if err != nil {
		return nil, err
	}
	public, ok := parsed.(*ecdsa.PublicKey)
	if !ok || public.Curve != elliptic.P256() {
		return nil, errors.New("aws kms: public key is not P-256")
	}
	return public, nil
}

// SignES256 signs the SHA256 digest once and converts KMS ASN.1 DER to the
// fixed-width 64-byte JOSE signature. key must identify an ECC_NIST_P256 key.
func (a KMS) SignES256(ctx context.Context, key string, message []byte) ([]byte, error) {
	if key == "" {
		return nil, errors.New("aws kms: key required")
	}
	// Validate key identity/curve before using the otherwise curve-agnostic KMS
	// ECDSA_SHA_256 algorithm. Use the returned immutable key ID for signing.
	out, err := a.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: sdk.String(key)})
	if err != nil {
		return nil, err
	}
	if key == "" || out == nil || out.KeyId == nil || *out.KeyId == "" || out.KeySpec != types.KeySpecEccNistP256 || out.KeyUsage != types.KeyUsageTypeSignVerify {
		return nil, errors.New("aws kms: ES256 requires resolved P-256 signing key")
	}
	digest := sha256.Sum256(message)
	result, err := a.Sign(ctx, &kms.SignInput{KeyId: out.KeyId, Message: digest[:], MessageType: types.MessageTypeDigest, SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256})
	if err != nil {
		return nil, err
	}
	if result == nil || result.KeyId == nil || *result.KeyId != *out.KeyId || result.SigningAlgorithm != types.SigningAlgorithmSpecEcdsaSha256 {
		return nil, errors.New("aws kms: missing signature response")
	}
	var signature ecdsaSignature
	rest, err := asn1.Unmarshal(result.Signature, &signature)
	if err != nil {
		return nil, err
	}
	if len(rest) != 0 || !validScalar(signature.R) || !validScalar(signature.S) {
		return nil, errors.New("aws kms: invalid P-256 signature")
	}
	raw := make([]byte, 64)
	signature.R.FillBytes(raw[:32])
	signature.S.FillBytes(raw[32:])
	return raw, nil
}

func validScalar(n *big.Int) bool {
	return n != nil && n.Sign() > 0 && n.Cmp(elliptic.P256().Params().N) < 0
}

// VerifyES256 uses the public key fetched from KMS and verifies the JOSE bytes
// locally. The raw Verify method separately exposes AWS KMS Verify semantics.
func (a KMS) VerifyES256(ctx context.Context, key string, message, signature []byte) error {
	if len(signature) != 64 {
		return errors.New("aws kms: ES256 signature must be 64 bytes")
	}
	public, err := a.ES256PublicKey(ctx, key)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(message)
	if !ecdsa.Verify(public, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return errors.New("aws kms: invalid ES256 signature")
	}
	return nil
}
