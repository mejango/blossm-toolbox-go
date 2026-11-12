package signing_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	token "github.com/mejango/blossm-toolbox-go/lib/jwt"
	"hash/crc32"
	"testing"
	"time"

	pb "cloud.google.com/go/kms/apiv1/kmspb"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
	awstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	gax "github.com/googleapis/gax-go/v2"
	cloudaws "github.com/mejango/blossm-toolbox-go/vendors/aws"
	cloudgcp "github.com/mejango/blossm-toolbox-go/vendors/gcp"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func testJWT(t *testing.T) (token.JWT[struct{}], token.Claims[struct{}]) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return token.JWT[struct{}]{Signer: token.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "issuer", Audience: "network", Now: func() time.Time { return time.Unix(100, 0) }}, token.Claims[struct{}]{Issuer: "issuer", Audience: []string{"network"}, Subject: "subject", Expires: 200, NotBefore: 100, ID: "id", Kind: "access"}
}

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
			key := jwt.Signer.(token.LocalSigner).Key
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			fixture := &signingFixture{t: t, key: key, ctx: ctx}
			if provider == "aws" {
				jwt.Signer = token.KMSSigner{Client: cloudaws.KMS{Client: awsSigningFixture{signingFixture: fixture}}, Key: "configured-key"}
			} else {
				version := "projects/project/locations/global/keyRings/ring/cryptoKeys/key/cryptoKeyVersions/1"
				jwt.Signer = token.KMSSigner{Client: cloudgcp.KMS{Client: gcpSigningFixture{signingFixture: fixture, version: version}}, Key: version}
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
