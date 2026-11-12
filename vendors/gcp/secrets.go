package gcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SecretCipher is deliberately the encrypted-object capability, not a universal
// cloud interface. Only ciphertext in base64 is persisted to GCS.
type SecretCipher interface {
	CreateKey(context.Context, string, string, bool, time.Duration, time.Time) error
	Encrypt(context.Context, string, []byte) ([]byte, error)
	Decrypt(context.Context, string, []byte) ([]byte, error)
}
type SecretObjects interface {
	Put(context.Context, string, string, []byte, string) error
	Get(context.Context, string, string) ([]byte, error)
}
type Secrets struct {
	KMS             SecretCipher
	Storage         SecretObjects
	Bucket, KeyRing string
}

func (s Secrets) key(ctx context.Context, id string) (string, error) {
	if err := ready(ctx, s.KMS); err != nil {
		return "", err
	}
	if err := ready(ctx, s.Storage); err != nil {
		return "", err
	}
	if s.Bucket == "" || s.KeyRing == "" || id == "" || strings.ContainsAny(id, "/\\") {
		return "", fmt.Errorf("gcp: bucket, key ring and simple secret ID required")
	}
	return s.KeyRing + "/cryptoKeys/" + id, nil
}
func (s Secrets) Create(ctx context.Context, id string, data []byte) error {
	name, err := s.key(ctx, id)
	if err != nil {
		return err
	}
	err = s.KMS.CreateKey(ctx, s.KeyRing, id, false, 0, time.Time{})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return err
	}
	ciphertext, err := s.KMS.Encrypt(ctx, name, data)
	if err != nil {
		return err
	}
	return s.Storage.Put(ctx, s.Bucket, id+".txt.encrypted", []byte(base64.StdEncoding.EncodeToString(ciphertext)), "text/plain")
}
func (s Secrets) Get(ctx context.Context, id string) ([]byte, error) {
	name, err := s.key(ctx, id)
	if err != nil {
		return nil, err
	}
	encoded, err := s.Storage.Get(ctx, s.Bucket, id+".txt.encrypted")
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("gcp: invalid encrypted secret encoding: %w", err)
	}
	return s.KMS.Decrypt(ctx, name, ciphertext)
}
