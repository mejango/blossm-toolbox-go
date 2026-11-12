package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// StorageAPI is the provider-specific injection boundary. *s3.Client implements it.
type StorageAPI interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
}

// Storage preserves AWS request, response, error and pagination semantics.
type Storage struct{ Client StorageAPI }

func (a Storage) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws s3.PutObject: client and input required")
	}
	return a.Client.PutObject(ctx, in, opts...)
}

func (a Storage) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws s3.GetObject: client and input required")
	}
	return a.Client.GetObject(ctx, in, opts...)
}

func (a Storage) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, opts ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws s3.ListObjectsV2: client and input required")
	}
	return a.Client.ListObjectsV2(ctx, in, opts...)
}

var _ StorageAPI = (*s3.Client)(nil)
