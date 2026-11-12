package aws

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// SecretsAPI is the provider-specific injection boundary. *secretsmanager.Client implements it.
type SecretsAPI interface {
	CreateSecret(context.Context, *secretsmanager.CreateSecretInput, ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(context.Context, *secretsmanager.PutSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	GetSecretValue(context.Context, *secretsmanager.GetSecretValueInput, ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error)
}

// Secrets preserves AWS request, response, error and pagination semantics.
type Secrets struct{ Client SecretsAPI }

func (a Secrets) CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws secretsmanager.CreateSecret: client and input required")
	}
	return a.Client.CreateSecret(ctx, in, opts...)
}

func (a Secrets) PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws secretsmanager.PutSecretValue: client and input required")
	}
	return a.Client.PutSecretValue(ctx, in, opts...)
}

func (a Secrets) GetSecretValue(ctx context.Context, in *secretsmanager.GetSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.GetSecretValueOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws secretsmanager.GetSecretValue: client and input required")
	}
	return a.Client.GetSecretValue(ctx, in, opts...)
}

var _ SecretsAPI = (*secretsmanager.Client)(nil)
