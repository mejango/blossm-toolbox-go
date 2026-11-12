package gcp

import (
	"bytes"
	kms "cloud.google.com/go/kms/apiv1"
	kp "cloud.google.com/go/kms/apiv1/kmspb"
	"cloud.google.com/go/storage"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// All HTTP fixture clients refuse redirects/dials to anything but their one
// loopback listener. SDK metrics are explicitly disabled for storage.
func localClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	u, err := url.Parse(server.URL)
	must(t, err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr != u.Host {
			return nil, fmt.Errorf("test blocked nonlocal dial %s", addr)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("test blocked redirect") }}
}
func TestStorageAndEncryptedSecretsWire(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	objects := map[string][]byte{}
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, `{"error":{"code":403,"message":"denied"}}`, 403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "POST" {
			typ, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if err != nil || !strings.HasPrefix(typ, "multipart/") {
				t.Errorf("upload not multipart: %s %v", typ, err)
				http.Error(w, "bad", 400)
				return
			}
			reader := multipart.NewReader(r.Body, params["boundary"])
			part, err := reader.NextPart()
			if err != nil {
				t.Error(err)
				http.Error(w, "bad", 400)
				return
			}
			var meta struct{ Name, CRC32C string }
			if err = json.NewDecoder(part).Decode(&meta); err != nil {
				t.Error(err)
			}
			part, err = reader.NextPart()
			if err != nil {
				t.Error(err)
				http.Error(w, "bad", 400)
				return
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Error(err)
			}
			var raw [4]byte
			binary.BigEndian.PutUint32(raw[:], crc32.Checksum(data, castagnoli))
			if meta.CRC32C != base64.StdEncoding.EncodeToString(raw[:]) {
				t.Errorf("missing upload CRC: %+v", meta)
			}
			objects[meta.Name] = data
			json.NewEncoder(w).Encode(map[string]any{"name": meta.Name, "bucket": "b", "size": fmt.Sprint(len(data)), "crc32c": meta.CRC32C})
			return
		}
		if r.URL.Query().Get("alt") == "media" {
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			data, ok := objects[name]
			if !ok {
				http.Error(w, "missing", 404)
				return
			}
			var raw [4]byte
			binary.BigEndian.PutUint32(raw[:], crc32.Checksum(data, castagnoli))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Goog-Hash", "crc32c="+base64.StdEncoding.EncodeToString(raw[:]))
			w.Write(data)
			return
		}
		if r.URL.Query().Get("pageToken") == "next" {
			io.WriteString(w, `{"items":[{"name":"second","bucket":"b"}]}`)
		} else {
			io.WriteString(w, `{"items":[{"name":"first","bucket":"b"}],"nextPageToken":"next"}`)
		}
	}))
	defer server.Close()
	client, err := storage.NewClient(ctx, option.WithEndpoint(server.URL), option.WithHTTPClient(localClient(t, server)), option.WithoutAuthentication(), storage.WithDisabledClientMetrics(), storage.WithJSONReads())
	must(t, err)
	defer client.Close()
	s := Storage{client}
	binaryData := []byte{0, 255, ' ', '\n'}
	must(t, s.Put(ctx, "b", "binary", binaryData, "application/octet-stream"))
	got, err := s.Get(ctx, "b", "binary")
	must(t, err)
	if !bytes.Equal(got, binaryData) {
		t.Fatal("binary changed")
	}
	page, next, err := s.ListPage(ctx, "b", "", "", 1)
	must(t, err)
	if len(page) != 1 || page[0].Name != "first" || next != "next" {
		t.Fatal(page, next)
	}
	page, next, err = s.ListPage(ctx, "b", "", next, 1)
	must(t, err)
	if len(page) != 1 || page[0].Name != "second" || next != "" {
		t.Fatal(page, next)
	}
	// Compose the actual KMS and storage adapters, preserving the retained base64
	// ciphertext object format without creating any plaintext temporary files.
	opts := wire(t, func(_ context.Context, _ string, v any) (any, error) {
		switch r := v.(type) {
		case *kp.CreateCryptoKeyRequest:
			if r.CryptoKeyId == "denied" {
				return nil, status.Error(codes.PermissionDenied, "denied")
			}
			return nil, status.Error(codes.AlreadyExists, "exists")
		case *kp.EncryptRequest:
			if !bytes.Equal(r.Plaintext, binaryData) {
				t.Error("secret plaintext changed")
			}
			cipher := []byte("encrypted bytes")
			return &kp.EncryptResponse{Name: version, Ciphertext: cipher, CiphertextCrc32C: checksum(cipher), VerifiedPlaintextCrc32C: true}, nil
		case *kp.DecryptRequest:
			if string(r.Ciphertext) != "encrypted bytes" {
				t.Error("ciphertext changed")
			}
			return &kp.DecryptResponse{Plaintext: binaryData, PlaintextCrc32C: checksum(binaryData)}, nil
		}
		return nil, status.Error(codes.Unimplemented, "unexpected")
	})
	kc, err := kms.NewKeyManagementClient(ctx, opts...)
	must(t, err)
	defer kc.Close()
	secrets := Secrets{KMS: KMS{kc}, Storage: s, Bucket: "b", KeyRing: ring}
	if err = secrets.Create(ctx, "denied", binaryData); status.Code(err) != codes.PermissionDenied {
		t.Fatal("secret key creation error hidden", err)
	}
	must(t, secrets.Create(ctx, "k", binaryData))
	got, err = secrets.Get(ctx, "k")
	must(t, err)
	if !bytes.Equal(got, binaryData) {
		t.Fatal("secret trimmed")
	}
	mu.Lock()
	stored := string(objects["k.txt.encrypted"])
	objects["k.txt.encrypted"] = []byte("!not-base64")
	mu.Unlock()
	if stored != base64.StdEncoding.EncodeToString([]byte("encrypted bytes")) {
		t.Fatal(stored)
	}
	if _, err = secrets.Get(ctx, "k"); err == nil {
		t.Fatal("invalid secret accepted")
	}
	fail.Store(true)
	if s.Put(ctx, "b", "denied", binaryData, "") == nil {
		t.Fatal("upload error hidden")
	}
	if _, err = s.Get(ctx, "b", "binary"); err == nil {
		t.Fatal("download error hidden")
	}
	if _, _, err = s.ListPage(ctx, "b", "", "", 1); err == nil {
		t.Fatal("list error hidden")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Get(canceled, "b", "binary"); err == nil {
		t.Fatal("cancellation hidden")
	}
}
func TestAuthSDKWire(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: mustPKCS8(t, private)})
	var fail atomic.Bool
	var requests atomic.Int32
	claims, _ := json.Marshal(map[string]any{"aud": "https://service.example", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()})
	token := "eyJhbGciOiJSUzI1NiJ9." + base64.RawURLEncoding.EncodeToString(claims) + ".c2ln"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if fail.Load() {
			http.Error(w, `{"error":"invalid_grant"}`, 400)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		parts := strings.Split(r.Form.Get("assertion"), ".")
		if len(parts) != 3 {
			t.Error("no signed assertion")
			http.Error(w, "bad assertion", 400)
			return
		}
		raw, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Error(err)
		}
		var c map[string]any
		if err = json.Unmarshal(raw, &c); err != nil {
			t.Error(err)
		}
		if c["target_audience"] != "https://service.example" || c["iss"] != "fixture@example.invalid" {
			t.Errorf("bad token claims %v", c)
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Error(err)
		}
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err = rsa.VerifyPKCS1v15(&private.PublicKey, crypto.SHA256, hash[:], signature); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"id_token": token})
	}))
	defer server.Close()
	credential, _ := json.Marshal(map[string]any{"type": "service_account", "client_email": "fixture@example.invalid", "private_key": string(keyPEM), "token_uri": server.URL})
	auth := Auth{Options: []option.ClientOption{option.WithAuthCredentialsJSON(idtoken.ServiceAccount, credential)}}
	ctx := context.WithValue(context.Background(), oauth2.HTTPClient, localClient(t, server))
	request, err := http.NewRequestWithContext(ctx, "POST", "https://service.example", nil)
	must(t, err)
	authorized, err := auth.Authorize(request, "https://service.example")
	must(t, err)
	if authorized.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("Authorization") != "" {
		t.Fatal("header contract")
	}
	fail.Store(true)
	if _, err = auth.Token(ctx, "https://service.example"); err == nil {
		t.Fatal("token endpoint error hidden")
	}
	before := requests.Load()
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = auth.Token(cancelCtx, "https://service.example"); err == nil {
		t.Fatal("cancellation hidden")
	}
	if requests.Load() != before {
		t.Fatal("canceled call hit server")
	}
}
func mustPKCS8(t *testing.T, key any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	must(t, err)
	return der
}
