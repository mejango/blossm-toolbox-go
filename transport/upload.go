package transport

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	cloudaws "blossm.local/backend/cloud/aws"
	cloudgcp "blossm.local/backend/cloud/gcp"
	"blossm.local/backend/dia"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type UploadLimits struct {
	BodyBytes, FileBytes, FieldBytes int64
	Files, Fields                    int
}
type UploadFile struct {
	Field, Name, ContentType string
	Data                     []byte
}
type UploadObject struct {
	Field       string `json:"field"`
	Name        string `json:"name"`
	Bucket      string `json:"bucket"`
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
}
type UploadForm struct {
	Fields  map[string]string `json:"fields"`
	Objects []UploadObject    `json:"objects"`
}

// UploadStore must select its own bucket/key from trusted configuration. On an
// uncertain failure return the attempted object reference alongside the error.
type UploadStore func(context.Context, Identity, UploadFile) (UploadObject, error)
type UploadGateway struct {
	Authorize func(*http.Request) (Identity, error)
	Limits    UploadLimits
	Store     UploadStore
	Handle    func(context.Context, Identity, UploadForm) (any, error)
}

func (g UploadGateway) Handler() (http.Handler, error) {
	l := g.Limits
	if g.Authorize == nil || g.Store == nil || l.BodyBytes <= 0 || l.FileBytes <= 0 || l.FieldBytes <= 0 || l.Files < 0 || l.Fields < 0 || l.FileBytes > l.BodyBytes || l.FieldBytes > l.BodyBytes {
		return nil, errors.New("upload requires authority, storage and bounded nonnegative counts/positive byte limits")
	}
	return wrap(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return nil
		}
		identity, err := g.Authorize(r)
		if err != nil {
			return err
		}
		// No object write happens until the complete multipart body passes limits.
		fields, files, err := parseGatewayUpload(w, r, l)
		if err != nil {
			return err
		}
		form := UploadForm{Fields: fields, Objects: []UploadObject{}}
		for _, file := range files {
			if err = r.Context().Err(); err != nil {
				return write(w, 504, map[string]any{"error": "upload interrupted", "objects": form.Objects})
			}
			object, err := g.Store(r.Context(), identity, file)
			if err != nil {
				return write(w, 502, map[string]any{"error": "object storage acceptance uncertain", "objects": form.Objects, "uncertain": object})
			}
			if object.Bucket == "" || object.Key == "" {
				return write(w, 502, map[string]any{"error": "object storage acknowledgment missing", "objects": form.Objects, "uncertain": object})
			}
			form.Objects = append(form.Objects, object)
		}
		if err := r.Context().Err(); err != nil {
			return write(w, 504, map[string]any{"error": "upload interrupted", "objects": form.Objects})
		}
		if g.Handle == nil {
			return write(w, 200, form)
		}
		out, err := g.Handle(r.Context(), identity, form)
		if err != nil {
			return write(w, 500, map[string]any{"error": "uploaded objects require recovery", "objects": form.Objects})
		}
		return write(w, 200, out)
	}), nil
}
func parseGatewayUpload(w http.ResponseWriter, r *http.Request, l UploadLimits) (map[string]string, []UploadFile, error) {
	r.Body = http.MaxBytesReader(w, r.Body, l.BodyBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return nil, nil, dia.ErrInvalid
	}
	fields := map[string]string{}
	var files []UploadFile
	seen := map[string]bool{}
	for {
		if err := r.Context().Err(); err != nil {
			return nil, nil, err
		}
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, dia.ErrInvalid
		}
		field := part.FormName()
		if field == "" || len(field) > 256 || seen[field] {
			part.Close()
			return nil, nil, dia.ErrInvalid
		}
		seen[field] = true
		isFile := part.FileName() != ""
		limit := l.FieldBytes
		if isFile {
			if len(files) >= l.Files {
				part.Close()
				return nil, nil, dia.ErrInvalid
			}
			limit = l.FileBytes
		} else if len(fields) >= l.Fields {
			part.Close()
			return nil, nil, dia.ErrInvalid
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		closeErr := part.Close()
		if err != nil || closeErr != nil || int64(len(data)) > limit {
			return nil, nil, dia.ErrInvalid
		}
		if !isFile {
			fields[field] = string(data)
			continue
		}
		contentType := part.Header.Get("Content-Type")
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		if _, _, err := mime.ParseMediaType(contentType); err != nil {
			return nil, nil, dia.ErrInvalid
		}
		files = append(files, UploadFile{Field: field, Name: part.FileName(), ContentType: contentType, Data: data})
	}
	// MultipartReader stops at the closing delimiter; enforce total wire limit on
	// epilogue bytes too, rather than allowing an unchecked trailing body.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return nil, nil, dia.ErrInvalid
	}
	return fields, files, nil
}
func uploadReference(bucket, prefix string, file UploadFile) (UploadObject, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return UploadObject{}, err
	}
	return UploadObject{Field: file.Field, Name: file.Name, Bucket: bucket, Key: prefix + hex.EncodeToString(random[:]), ContentType: file.ContentType, Size: int64(len(file.Data))}, nil
}
func uploadDestination(bucket, prefix string) error {
	if bucket == "" || strings.ContainsAny(prefix, "\x00\r\n") || strings.HasPrefix(prefix, "/") {
		return errors.New("upload bucket and relative object prefix required")
	}
	for _, part := range strings.Split(prefix, "/") {
		if part == "." || part == ".." {
			return errors.New("invalid upload prefix")
		}
	}
	return nil
}
func GCPUploadStorage(storage cloudgcp.Storage, bucket, prefix string) (UploadStore, error) {
	if err := uploadDestination(bucket, prefix); err != nil {
		return nil, err
	}
	return func(ctx context.Context, _ Identity, file UploadFile) (UploadObject, error) {
		object, err := uploadReference(bucket, prefix, file)
		if err != nil {
			return object, err
		}
		return object, storage.Put(ctx, bucket, object.Key, file.Data, file.ContentType)
	}, nil
}
func AWSUploadStorage(storage cloudaws.Storage, bucket, prefix string) (UploadStore, error) {
	if err := uploadDestination(bucket, prefix); err != nil {
		return nil, err
	}
	return func(ctx context.Context, _ Identity, file UploadFile) (UploadObject, error) {
		object, err := uploadReference(bucket, prefix, file)
		if err != nil {
			return object, err
		}
		out, err := storage.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(object.Key), Body: bytes.NewReader(file.Data), ContentType: aws.String(file.ContentType), ContentLength: aws.Int64(int64(len(file.Data)))})
		if err == nil && out == nil {
			err = errors.New("missing object acknowledgment")
		}
		return object, err
	}, nil
}
