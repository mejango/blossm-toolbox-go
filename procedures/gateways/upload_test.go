package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	cloudaws "github.com/mejango/blossm-toolbox-go/vendors/aws"
	cloudgcp "github.com/mejango/blossm-toolbox-go/vendors/gcp"
	"google.golang.org/api/option"
)

func uploadBody(t *testing.T, fields map[string]string, files map[string]string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for field, value := range fields {
		if err := writer.WriteField(field, value); err != nil {
			t.Fatal(err)
		}
	}
	for field, value := range files {
		part, err := writer.CreateFormFile(field, "../../file.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(part, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}
func uploadHTTP(h http.Handler, body []byte, contentType string, authorized bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/upload", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	if authorized {
		r.Header.Set("Authorization", "Bearer fixture")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestGatewayMultipartPreparseBounds(t *testing.T) {
	defaults := UploadLimits{BodyBytes: 4096, FileBytes: 8, FieldBytes: 8, Files: 2, Fields: 2}
	for _, mode := range []string{"valid", "auth", "file-bytes", "file-count", "field-bytes", "field-count", "total-bytes", "epilogue", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			limits := defaults
			fields := map[string]string{"payload": "hello"}
			files := map[string]string{"file": "hello"}
			switch mode {
			case "file-bytes":
				limits.FileBytes = 4
			case "file-count":
				limits.Files = 0
			case "field-bytes":
				limits.FieldBytes = 4
			case "field-count":
				limits.Fields = 0
			case "total-bytes":
				limits.BodyBytes = 20
			}
			body, contentType := uploadBody(t, fields, files)
			if mode == "malformed" {
				body = body[:len(body)-12]
			}
			if mode == "epilogue" {
				body = append(body, bytes.Repeat([]byte("x"), 4096)...)
			}
			calls := 0
			g := UploadGateway{Authorize: func(r *http.Request) (Identity, error) {
				return (Authenticator{Bearer: fixtureVerifier}).Authenticate(r)
			}, Limits: limits, Store: func(ctx context.Context, id Identity, file UploadFile) (UploadObject, error) {
				calls++
				if string(file.Data) != "hello" || file.Name != "file.txt" {
					t.Fatal("file bytes/path", string(file.Data), file.Name)
				}
				return UploadObject{Bucket: "bucket", Key: "server-key", Name: file.Name, Size: int64(len(file.Data))}, nil
			}}
			h, err := g.Handler()
			if err != nil {
				t.Fatal(err)
			}
			w := uploadHTTP(h, body, contentType, mode != "auth")
			if mode == "valid" {
				if w.Code != 200 || calls != 1 || !strings.Contains(w.Body.String(), "server-key") {
					t.Fatal(w.Code, calls, w.Body.String())
				}
			} else if w.Code < 400 || calls != 0 {
				t.Fatal("invalid upload reached storage", w.Code, calls)
			}
		})
	}
}
func TestGatewayUploadRecoveryAndCancellation(t *testing.T) {
	body, contentType := uploadBody(t, nil, map[string]string{"a": "one", "b": "two"})
	calls := 0
	g := UploadGateway{Authorize: func(r *http.Request) (Identity, error) {
		return (Authenticator{Bearer: fixtureVerifier}).Authenticate(r)
	}, Limits: UploadLimits{BodyBytes: 4096, FileBytes: 8, FieldBytes: 8, Files: 2, Fields: 2}, Store: func(ctx context.Context, id Identity, file UploadFile) (UploadObject, error) {
		calls++
		object := UploadObject{Bucket: "bucket", Key: file.Field}
		if calls == 2 {
			return object, errors.New("secret provider failure")
		}
		return object, nil
	}}
	h, _ := g.Handler()
	w := uploadHTTP(h, body, contentType, true)
	if w.Code != 502 || calls != 2 || !strings.Contains(w.Body.String(), `"uncertain"`) || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, calls, w.Body.String())
	}
	calls = 0
	g.Store = func(ctx context.Context, id Identity, file UploadFile) (UploadObject, error) {
		calls++
		return UploadObject{}, nil
	}
	h, _ = g.Handler()
	w = uploadHTTP(h, body, contentType, true)
	if w.Code != 502 || calls != 1 {
		t.Fatal("zero acknowledgment accepted", w.Code, calls)
	}
	r := httptest.NewRequest("POST", "/upload", bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	r.Header.Set("Authorization", "Bearer fixture")
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	calls = 0
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if calls != 0 || w.Code < 400 {
		t.Fatal("cancelled request wrote object")
	}
}
func TestGatewayAWSUploadAdapterUsesOfficialStorage(t *testing.T) {
	var key string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PUT" || !strings.HasPrefix(r.URL.Path, "/bucket/prefix/") {
			t.Error("wrong object target", r.Method, r.URL.Path)
		}
		key = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "content" {
			t.Error("object bytes", string(body), err)
		}
		w.Header().Set("ETag", `"fixture"`)
		w.WriteHeader(200)
	}))
	defer server.Close()
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", HTTPClient: server.Client(), Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "fixture", SecretAccessKey: "fixture"}, nil
	})}, func(o *s3.Options) { o.BaseEndpoint = aws.String(server.URL); o.UsePathStyle = true })
	store, err := AWSUploadStorage(cloudaws.Storage{Client: client}, "bucket", "prefix/")
	if err != nil {
		t.Fatal(err)
	}
	object, err := store(context.Background(), authIdentity(), UploadFile{Name: "../../hostile", Field: "f", ContentType: "text/plain", Data: []byte("content")})
	if err != nil || object.Size != 7 || key != "/bucket/"+object.Key || strings.Contains(object.Key, "hostile") {
		t.Fatal(object, err)
	}
}

func TestGatewayGCPUploadAdapterUsesOfficialStorage(t *testing.T) {
	var objectName string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		metadata, err := reader.NextPart()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		var meta struct {
			Name   string `json:"name"`
			CRC32C string `json:"crc32c"`
		}
		if err = json.NewDecoder(metadata).Decode(&meta); err != nil {
			t.Error(err)
		}
		dataPart, err := reader.NextPart()
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		data, err := io.ReadAll(dataPart)
		if err != nil || !bytes.Equal(data, []byte{0, 255, 1, 128}) {
			t.Error("upload bytes", data, err)
		}
		objectName = meta.Name
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"name": meta.Name, "bucket": "bucket", "size": "4", "crc32c": meta.CRC32C})
	}))
	defer server.Close()
	client, err := storage.NewClient(context.Background(), option.WithEndpoint(server.URL), option.WithHTTPClient(server.Client()), option.WithoutAuthentication(), storage.WithDisabledClientMetrics())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	store, err := GCPUploadStorage(cloudgcp.Storage{Client: client}, "bucket", "prefix/")
	if err != nil {
		t.Fatal(err)
	}
	object, err := store(context.Background(), authIdentity(), UploadFile{Name: "../../hostile", Field: "f", ContentType: "application/octet-stream", Data: []byte{0, 255, 1, 128}})
	if err != nil || object.Size != 4 || object.Key != objectName || !strings.HasPrefix(object.Key, "prefix/") || strings.Contains(object.Key, "hostile") {
		t.Fatal(object, err)
	}
}
