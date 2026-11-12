package gcp

import (
	pb "cloud.google.com/go/kms/apiv1/kmspb"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"
	"hash/crc32"
	"math/big"
	"regexp"
	"strings"
	"time"
)

type KMSAPI interface {
	Encrypt(context.Context, *pb.EncryptRequest, ...gax.CallOption) (*pb.EncryptResponse, error)
	Decrypt(context.Context, *pb.DecryptRequest, ...gax.CallOption) (*pb.DecryptResponse, error)
	AsymmetricSign(context.Context, *pb.AsymmetricSignRequest, ...gax.CallOption) (*pb.AsymmetricSignResponse, error)
	GetPublicKey(context.Context, *pb.GetPublicKeyRequest, ...gax.CallOption) (*pb.PublicKey, error)
	CreateCryptoKey(context.Context, *pb.CreateCryptoKeyRequest, ...gax.CallOption) (*pb.CryptoKey, error)
	GetCryptoKey(context.Context, *pb.GetCryptoKeyRequest, ...gax.CallOption) (*pb.CryptoKey, error)
	CreateKeyRing(context.Context, *pb.CreateKeyRingRequest, ...gax.CallOption) (*pb.KeyRing, error)
	GetKeyRing(context.Context, *pb.GetKeyRingRequest, ...gax.CallOption) (*pb.KeyRing, error)
}
type KMS struct{ Client KMSAPI }

var ErrIntegrity = errors.New("gcp: KMS integrity verification failed")
var versionName = regexp.MustCompile(`^projects/[^/]+/locations/[^/]+/keyRings/[^/]+/cryptoKeys/[^/]+/cryptoKeyVersions/[1-9][0-9]*$`)
var castagnoli = crc32.MakeTable(crc32.Castagnoli)

func checksum(data []byte) *wrapperspb.Int64Value {
	return wrapperspb.Int64(int64(crc32.Checksum(data, castagnoli)))
}
func validChecksum(data []byte, c *wrapperspb.Int64Value) bool {
	return c != nil && c.Value == checksum(data).Value
}
func (k KMS) Encrypt(ctx context.Context, name string, data []byte) ([]byte, error) {
	if err := ready(ctx, k.Client); err != nil {
		return nil, err
	}
	r, err := k.Client.Encrypt(ctx, &pb.EncryptRequest{Name: name, Plaintext: data, PlaintextCrc32C: checksum(data)})
	if err != nil {
		return nil, err
	}
	if r == nil || !versionName.MatchString(r.Name) || !strings.HasPrefix(r.Name, name+"/cryptoKeyVersions/") || !r.VerifiedPlaintextCrc32C || !validChecksum(r.Ciphertext, r.CiphertextCrc32C) {
		return nil, ErrIntegrity
	}
	return r.Ciphertext, nil
}
func (k KMS) Decrypt(ctx context.Context, name string, data []byte) ([]byte, error) {
	if err := ready(ctx, k.Client); err != nil {
		return nil, err
	}
	r, err := k.Client.Decrypt(ctx, &pb.DecryptRequest{Name: name, Ciphertext: data, CiphertextCrc32C: checksum(data)})
	if err != nil {
		return nil, err
	}
	if r == nil || !validChecksum(r.Plaintext, r.PlaintextCrc32C) {
		return nil, ErrIntegrity
	}
	return r.Plaintext, nil
}

// Sign hashes bytes once with SHA-256 and returns KMS-native signature bytes
// (ASN.1 DER for EC keys). Use SignES256 for JWT's JOSE R||S representation.
func (k KMS) Sign(ctx context.Context, version string, message []byte) ([]byte, error) {
	if err := ready(ctx, k.Client); err != nil {
		return nil, err
	}
	if !versionName.MatchString(version) {
		return nil, fmt.Errorf("gcp: explicit numeric key version required")
	}
	digest := sha256.Sum256(message)
	r, err := k.Client.AsymmetricSign(ctx, &pb.AsymmetricSignRequest{Name: version, Digest: &pb.Digest{Digest: &pb.Digest_Sha256{Sha256: digest[:]}}, DigestCrc32C: checksum(digest[:])})
	if err != nil {
		return nil, err
	}
	if r == nil || r.Name != version || !r.VerifiedDigestCrc32C || !validChecksum(r.Signature, r.SignatureCrc32C) {
		return nil, ErrIntegrity
	}
	return r.Signature, nil
}

// PublicKey does not cache aliases or PEMs. Each request uses the complete key
// version identity, including project and location.
func (k KMS) PublicKey(ctx context.Context, version string) (string, error) {
	if err := ready(ctx, k.Client); err != nil {
		return "", err
	}
	if !versionName.MatchString(version) {
		return "", fmt.Errorf("gcp: explicit numeric key version required")
	}
	r, err := k.Client.GetPublicKey(ctx, &pb.GetPublicKeyRequest{Name: version})
	if err != nil {
		return "", err
	}
	if r == nil || r.Name != version || !validChecksum([]byte(r.Pem), r.PemCrc32C) {
		return "", ErrIntegrity
	}
	return r.Pem, nil
}
func (k KMS) ecKey(ctx context.Context, version string) (*ecdsa.PublicKey, error) {
	text, err := k.PublicKey(ctx, version)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode([]byte(text))
	if block == nil || len(rest) != 0 {
		return nil, fmt.Errorf("gcp: invalid public key PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P256() {
		return nil, fmt.Errorf("gcp: expected P-256 signing key")
	}
	return ec, nil
}

// Verify checks a native DER P-256/SHA-256 signature. Malformed signatures fail.
func (k KMS) Verify(ctx context.Context, version string, message, signature []byte) (bool, error) {
	key, err := k.ecKey(ctx, version)
	if err != nil {
		return false, err
	}
	d := sha256.Sum256(message)
	return ecdsa.VerifyASN1(key, d[:], signature), nil
}
func (k KMS) SignES256(ctx context.Context, version string, message []byte) ([]byte, error) {
	if _, err := k.ecKey(ctx, version); err != nil {
		return nil, err
	}
	der, err := k.Sign(ctx, version, message)
	if err != nil {
		return nil, err
	}
	var sig struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &sig)
	if err != nil || len(rest) != 0 || sig.R == nil || sig.S == nil || sig.R.Sign() <= 0 || sig.S.Sign() <= 0 || sig.R.Cmp(elliptic.P256().Params().N) >= 0 || sig.S.Cmp(elliptic.P256().Params().N) >= 0 {
		return nil, fmt.Errorf("gcp: malformed P-256 DER signature")
	}
	out := make([]byte, 64)
	sig.R.FillBytes(out[:32])
	sig.S.FillBytes(out[32:])
	return out, nil
}
func (k KMS) VerifyES256(ctx context.Context, version string, message, signature []byte) (bool, error) {
	key, err := k.ecKey(ctx, version)
	if err != nil {
		return false, err
	}
	if len(signature) != 64 {
		return false, nil
	}
	d := sha256.Sum256(message)
	return ecdsa.Verify(key, d[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])), nil
}
func (k KMS) KeyExists(ctx context.Context, name string) (bool, error) {
	if err := ready(ctx, k.Client); err != nil {
		return false, err
	}
	_, err := k.Client.GetCryptoKey(ctx, &pb.GetCryptoKeyRequest{Name: name})
	return exists(err)
}
func (k KMS) KeyRingExists(ctx context.Context, name string) (bool, error) {
	if err := ready(ctx, k.Client); err != nil {
		return false, err
	}
	_, err := k.Client.GetKeyRing(ctx, &pb.GetKeyRingRequest{Name: name})
	return exists(err)
}
func (k KMS) CreateKeyRing(ctx context.Context, parent, id string) error {
	if err := ready(ctx, k.Client); err != nil {
		return err
	}
	_, err := k.Client.CreateKeyRing(ctx, &pb.CreateKeyRingRequest{Parent: parent, KeyRingId: id, KeyRing: &pb.KeyRing{}})
	return err
}

// CreateKey supports retained symmetric rotation and P-256 signing resources.
// Existing keys are not silently accepted or reconfigured.
func (k KMS) CreateKey(ctx context.Context, ring, id string, signing bool, rotation time.Duration, next time.Time) error {
	if err := ready(ctx, k.Client); err != nil {
		return err
	}
	key := &pb.CryptoKey{Purpose: pb.CryptoKey_ENCRYPT_DECRYPT}
	if signing {
		if rotation != 0 || !next.IsZero() {
			return fmt.Errorf("gcp: automatic rotation only supported for symmetric keys")
		}
		key.Purpose = pb.CryptoKey_ASYMMETRIC_SIGN
		key.VersionTemplate = &pb.CryptoKeyVersionTemplate{Algorithm: pb.CryptoKeyVersion_EC_SIGN_P256_SHA256}
	}
	if rotation != 0 || !next.IsZero() {
		if rotation <= 0 || next.IsZero() {
			return fmt.Errorf("gcp: rotation duration and next time required together")
		}
		key.RotationSchedule = &pb.CryptoKey_RotationPeriod{RotationPeriod: durationpb.New(rotation)}
		key.NextRotationTime = timestamppb.New(next)
		if err := key.NextRotationTime.CheckValid(); err != nil {
			return err
		}
	}
	_, err := k.Client.CreateCryptoKey(ctx, &pb.CreateCryptoKeyRequest{Parent: ring, CryptoKeyId: id, CryptoKey: key})
	return err
}
