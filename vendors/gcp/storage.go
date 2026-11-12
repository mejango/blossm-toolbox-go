package gcp

import (
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
)

// StorageAPI is the official SDK seam; tests inject real SDK clients pointed at
// loopback servers. No default client or credential discovery happens here.
type StorageAPI interface {
	Bucket(string) *storage.BucketHandle
}
type Storage struct{ Client StorageAPI }

func (s Storage) Put(ctx context.Context, bucket, name string, data []byte, contentType string) error {
	if err := ready(ctx, s.Client); err != nil {
		return err
	}
	if bucket == "" || name == "" {
		return fmt.Errorf("gcp: bucket and object name required")
	}
	writerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := s.Client.Bucket(bucket).Object(name).NewWriter(writerCtx)
	w.ContentType = contentType
	w.CRC32C = crc32.Checksum(data, castagnoli)
	w.SendCRC32C = true
	if _, err := w.Write(data); err != nil {
		cancel()
		_ = w.Close()
		return err
	}
	return w.Close()
}
func (s Storage) Get(ctx context.Context, bucket, name string) ([]byte, error) {
	if err := ready(ctx, s.Client); err != nil {
		return nil, err
	}
	if bucket == "" || name == "" {
		return nil, fmt.Errorf("gcp: bucket and object name required")
	}
	r, err := s.Client.Bucket(bucket).Object(name).NewReader(ctx)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(r)
	closeErr := r.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	return data, nil
}

// ListPage exposes pagination explicitly so permissions lookup need not buffer
// every object. Pass the returned token to fetch subsequent pages.
func (s Storage) ListPage(ctx context.Context, bucket, prefix, token string, size int) ([]*storage.ObjectAttrs, string, error) {
	if err := ready(ctx, s.Client); err != nil {
		return nil, "", err
	}
	if bucket == "" || size <= 0 {
		return nil, "", fmt.Errorf("gcp: bucket and positive page size required")
	}
	it := s.Client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: prefix})
	var objects []*storage.ObjectAttrs
	next, err := iterator.NewPager(it, size, token).NextPage(&objects)
	return objects, next, err
}
