package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	gax "github.com/googleapis/gax-go/v2"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	pb "cloud.google.com/go/kms/apiv1/kmspb"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	awstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	cloudaws "github.com/mejango/blossm-toolbox-go/cloud/aws"
	cloudgcp "github.com/mejango/blossm-toolbox-go/cloud/gcp"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

type signingFixture struct {
	t         *testing.T
	key       *ecdsa.PrivateKey
	ctx       context.Context
	calls     int
	failure   error
	malformed bool
}

func (f *signingFixture) check(ctx context.Context) error {
	f.t.Helper()
	f.calls++
	if ctx != f.ctx {
		f.t.Fatal("request context lost")
	}
	return f.failure
}
func (f *signingFixture) signature(digest []byte) ([]byte, error) {
	if f.malformed {
		return []byte("bad DER"), nil
	}
	return ecdsa.SignASN1(rand.Reader, f.key, digest)
}
func (f *signingFixture) der() []byte {
	b, e := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	if e != nil {
		f.t.Fatal(e)
	}
	return b
}

type awsSigningFixture struct {
	cloudaws.KMSAPI
	*signingFixture
}

func (f awsSigningFixture) GetPublicKey(ctx context.Context, in *awskms.GetPublicKeyInput, _ ...func(*awskms.Options)) (*awskms.GetPublicKeyOutput, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	if sdk.ToString(in.KeyId) != "configured-key" {
		f.t.Fatal("key identity lost")
	}
	return &awskms.GetPublicKeyOutput{KeyId: sdk.String("immutable-key"), KeySpec: awstypes.KeySpecEccNistP256, KeyUsage: awstypes.KeyUsageTypeSignVerify, PublicKey: f.der()}, nil
}
func (f awsSigningFixture) Sign(ctx context.Context, in *awskms.SignInput, _ ...func(*awskms.Options)) (*awskms.SignOutput, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	if sdk.ToString(in.KeyId) != "immutable-key" || in.MessageType != awstypes.MessageTypeDigest || in.SigningAlgorithm != awstypes.SigningAlgorithmSpecEcdsaSha256 || len(in.Message) != sha256.Size {
		f.t.Fatal("incorrect AWS signing protocol")
	}
	signature, err := f.signature(in.Message)
	return &awskms.SignOutput{KeyId: in.KeyId, Signature: signature, SigningAlgorithm: in.SigningAlgorithm}, err
}

type gcpSigningFixture struct {
	cloudgcp.KMSAPI
	*signingFixture
	version string
}

func crc(data []byte) *wrapperspb.Int64Value {
	return wrapperspb.Int64(int64(crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli))))
}
func (f gcpSigningFixture) GetPublicKey(ctx context.Context, in *pb.GetPublicKeyRequest, _ ...gax.CallOption) (*pb.PublicKey, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	if in.Name != f.version {
		f.t.Fatal("version identity lost")
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: f.der()})
	return &pb.PublicKey{Name: f.version, Pem: string(encoded), PemCrc32C: crc(encoded), Algorithm: pb.CryptoKeyVersion_EC_SIGN_P256_SHA256}, nil
}
func (f gcpSigningFixture) AsymmetricSign(ctx context.Context, in *pb.AsymmetricSignRequest, _ ...gax.CallOption) (*pb.AsymmetricSignResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	digest := in.Digest.GetSha256()
	if in.Name != f.version || len(digest) != sha256.Size || in.DigestCrc32C.GetValue() != crc(digest).Value {
		f.t.Fatal("incorrect GCP signing protocol")
	}
	signature, err := f.signature(digest)
	return &pb.AsymmetricSignResponse{Name: f.version, Signature: signature, SignatureCrc32C: crc(signature), VerifiedDigestCrc32C: true}, err
}

func TestJWTCloudSigningAdapters(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		t.Run(provider, func(t *testing.T) {
			jwt, claims := testJWT(t)
			key := jwt.Signer.(LocalSigner).Key
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			fixture := &signingFixture{t: t, key: key, ctx: ctx}
			if provider == "aws" {
				jwt.Signer = KMSSigner{Client: cloudaws.KMS{Client: awsSigningFixture{signingFixture: fixture}}, Key: "configured-key"}
			} else {
				version := "projects/project/locations/global/keyRings/ring/cryptoKeys/key/cryptoKeyVersions/1"
				jwt.Signer = KMSSigner{Client: cloudgcp.KMS{Client: gcpSigningFixture{signingFixture: fixture, version: version}}, Key: version}
			}
			token, err := jwt.Sign(ctx, claims)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = jwt.Verify(token, "access"); err != nil {
				t.Fatal(err)
			}
			if fixture.calls != 2 {
				t.Fatal("adapter not invoked", fixture.calls)
			}
			verifier := jwt
			verifier.Signer = nil
			if _, err = verifier.Verify(token, "access"); err != nil {
				t.Fatal(err)
			}
			if _, err = verifier.PublicKey(); err != nil {
				t.Fatal(err)
			}
			if fixture.calls != 2 {
				t.Fatal("verification called provider")
			}
			// Provider rotation never changes the configured verification trust.
			rotated, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			fixture.key = rotated
			if token, err = jwt.Sign(ctx, claims); err == nil || token != "" {
				t.Fatal("issued wrong-key signature")
			}
			fixture.key = key
			fixture.malformed = true
			if token, err = jwt.Sign(ctx, claims); err == nil || token != "" {
				t.Fatal("issued malformed provider signature")
			}
			fixture.malformed = false
			offline := errors.New("provider unavailable")
			fixture.failure = offline
			if token, err = jwt.Sign(ctx, claims); !errors.Is(err, offline) || token != "" {
				t.Fatal("provider failure hidden", err)
			}
			before := fixture.calls
			cancel()
			if token, err = jwt.Sign(ctx, claims); !errors.Is(err, context.Canceled) || token != "" || fixture.calls != before {
				t.Fatal("cancellation ignored", err)
			}
		})
	}
}

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
