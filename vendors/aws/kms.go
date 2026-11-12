package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// KMSAPI is the provider-specific injection boundary. *kms.Client implements it.
type KMSAPI interface {
	CreateKey(context.Context, *kms.CreateKeyInput, ...func(*kms.Options)) (*kms.CreateKeyOutput, error)
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
	Verify(context.Context, *kms.VerifyInput, ...func(*kms.Options)) (*kms.VerifyOutput, error)
	Encrypt(context.Context, *kms.EncryptInput, ...func(*kms.Options)) (*kms.EncryptOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

// KMS preserves AWS request, response, error and pagination semantics.
type KMS struct{ Client KMSAPI }

func (a KMS) CreateKey(ctx context.Context, in *kms.CreateKeyInput, opts ...func(*kms.Options)) (*kms.CreateKeyOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws kms.CreateKey: client and input required")
	}
	return a.Client.CreateKey(ctx, in, opts...)
}

func (a KMS) Sign(ctx context.Context, in *kms.SignInput, opts ...func(*kms.Options)) (*kms.SignOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws kms.Sign: client and input required")
	}
	return a.Client.Sign(ctx, in, opts...)
}

func (a KMS) Verify(ctx context.Context, in *kms.VerifyInput, opts ...func(*kms.Options)) (*kms.VerifyOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws kms.Verify: client and input required")
	}
	return a.Client.Verify(ctx, in, opts...)
}

func (a KMS) Encrypt(ctx context.Context, in *kms.EncryptInput, opts ...func(*kms.Options)) (*kms.EncryptOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws kms.Encrypt: client and input required")
	}
	return a.Client.Encrypt(ctx, in, opts...)
}

func (a KMS) Decrypt(ctx context.Context, in *kms.DecryptInput, opts ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws kms.Decrypt: client and input required")
	}
	return a.Client.Decrypt(ctx, in, opts...)
}

func (a KMS) GetPublicKey(ctx context.Context, in *kms.GetPublicKeyInput, opts ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws kms.GetPublicKey: client and input required")
	}
	return a.Client.GetPublicKey(ctx, in, opts...)
}

var _ KMSAPI = (*kms.Client)(nil)
