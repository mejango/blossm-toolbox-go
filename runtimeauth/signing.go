// Package runtimeauth composes pinned JWT trust and contextual signing adapters.
package runtimeauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"

	cloudaws "blossm.local/backend/cloud/aws"
	cloudgcp "blossm.local/backend/cloud/gcp"
	"blossm.local/backend/dia"
	"blossm.local/backend/runtimeenv"
	gcpkms "cloud.google.com/go/kms/apiv1"
	"github.com/aws/aws-sdk-go-v2/config"
	awskms "github.com/aws/aws-sdk-go-v2/service/kms"
)

// Open selects one signing owner. Verification uses configured trust and never
// adopts a provider key implicitly. No provider signing occurs during startup.
func Open(ctx context.Context, network string) (*dia.JWT, func(), error) {
	return configuredJWT(ctx, network, openKMS)
}

type kmsFactory func(context.Context, string) (dia.KMSClient, func(), error)

func configuredJWT(ctx context.Context, network string, open kmsFactory) (*dia.JWT, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	result := &dia.JWT{Issuer: "dia." + network, Audience: network}
	close := func() {}
	switch os.Getenv("JWT_SIGNER") {
	case "local":
		if os.Getenv("JWT_KMS_KEY") != "" || os.Getenv("JWT_PUBLIC_KEY") != "" {
			return nil, nil, errors.New("local signing requires only JWT_PRIVATE_KEY")
		}
		encoded, err := runtimeenv.Required("JWT_PRIVATE_KEY")
		if err != nil {
			return nil, nil, err
		}
		block, rest := pem.Decode([]byte(encoded))
		if block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
			return nil, nil, errors.New("JWT_PRIVATE_KEY must be one PKCS8 PEM key")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, nil, errors.New("invalid signing key")
		}
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok || key.Curve != elliptic.P256() {
			return nil, nil, errors.New("P256 ECDSA key required")
		}
		result.Signer = dia.LocalSigner{Key: key}
		result.VerifyKey = &key.PublicKey
	case "gcp", "aws":
		if os.Getenv("JWT_PRIVATE_KEY") != "" {
			return nil, nil, errors.New("cloud signing forbids JWT_PRIVATE_KEY")
		}
		key, err := runtimeenv.Required("JWT_KMS_KEY")
		if err != nil {
			return nil, nil, err
		}
		encoded, err := runtimeenv.Required("JWT_PUBLIC_KEY")
		if err != nil {
			return nil, nil, err
		}
		block, rest := pem.Decode([]byte(encoded))
		if block == nil || block.Type != "PUBLIC KEY" || len(rest) != 0 {
			return nil, nil, errors.New("JWT_PUBLIC_KEY must be one PKIX PEM key")
		}
		parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, nil, errors.New("invalid verification key")
		}
		public, ok := parsed.(*ecdsa.PublicKey)
		if !ok || public.Curve != elliptic.P256() {
			return nil, nil, errors.New("P256 verification key required")
		}
		result.VerifyKey = public
		if _, err = result.PublicKey(); err != nil {
			return nil, nil, err
		}
		client, cleanup, err := open(ctx, os.Getenv("JWT_SIGNER"))
		if err != nil {
			return nil, nil, err
		}
		if client == nil || cleanup == nil {
			if cleanup != nil {
				cleanup()
			}
			return nil, nil, errors.New("invalid KMS runtime")
		}
		close = cleanup
		result.Signer = dia.KMSSigner{Client: client, Key: key}
	default:
		return nil, nil, errors.New("JWT_SIGNER must be local, gcp or aws")
	}
	return result, close, nil
}

func openKMS(ctx context.Context, provider string) (dia.KMSClient, func(), error) {
	switch provider {
	case "gcp":
		client, err := gcpkms.NewKeyManagementClient(ctx)
		if err != nil {
			return nil, nil, err
		}
		return cloudgcp.KMS{Client: client}, func() { _ = client.Close() }, nil
	case "aws":
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, nil, err
		}
		return cloudaws.KMS{Client: awskms.NewFromConfig(cfg)}, func() {}, nil
	default:
		return nil, nil, errors.New("unsupported KMS provider")
	}
}
