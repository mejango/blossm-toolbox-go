package runtimepub

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
)

// Never consult ambient credential files or metadata. Tests using process
// environment are serial; fake credentials are constructor inputs only.
func isolateOpenEnvironment(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "AWS_") || strings.HasPrefix(key, "GOOGLE_") || strings.HasPrefix(key, "GCE_") || strings.HasPrefix(key, "PUBSUB_") || strings.HasPrefix(key, "PUBLICATION_") {
			t.Setenv(key, "")
		}
	}
	directory := t.TempDir()
	for _, name := range []string{"config", "credentials"} {
		if err := os.WriteFile(filepath.Join(directory, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(directory, "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(directory, "credentials"))
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_DEFAULTS_MODE", "standard")
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "fixture-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fixture-secret")
	adc := filepath.Join(directory, "invalid-adc.json")
	if err := os.WriteFile(adc, []byte(`{"type":"invalid-fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", adc)
	t.Setenv("PUBLICATION_TOPIC", "fixture-topic")
	t.Setenv("PUBLICATION_SUBSCRIPTION", "fixture-subscription")
	t.Setenv("PUBLICATION_QUEUE_URL", "https://sqs.us-east-1.amazonaws.com/123456789012/fixture")
}

func TestOpenConfigurationFailures(t *testing.T) {
	for _, tc := range []struct{ name, provider, key, value string }{
		{"missing topic", "aws", "PUBLICATION_TOPIC", ""},
		{"missing subscription", "gcp", "PUBLICATION_SUBSCRIPTION", ""},
		{"missing queue", "aws", "PUBLICATION_QUEUE_URL", ""},
		{"unsupported provider", "unknown", "", ""},
		{"invalid AWS configuration", "aws", "AWS_RETRY_MODE", "invalid-fixture"},
		{"invalid explicit GCP credentials", "gcp", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateOpenEnvironment(t)
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			}
			p, consume, closeFn, err := Open(context.Background(), tc.provider)
			if err == nil || p != nil || consume != nil || closeFn != nil {
				if closeFn != nil {
					closeFn()
				}
				t.Fatalf("constructor failure returned resources: %T %v", p, err)
			}
		})
	}
}
func TestOpenAWSOfficialConfigAndClose(t *testing.T) {
	isolateOpenEnvironment(t)
	p, consume, closeFn, err := Open(context.Background(), "aws")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || consume == nil || closeFn == nil {
		t.Fatal("incomplete runtime")
	}
	closeFn()
	closeFn()
	if err := p.Publish(context.Background(), validNotice(t)); !errors.Is(err, ErrClosed) {
		t.Fatal("Open cleanup did not close runtime", err)
	}
}
func TestOpenGCPEmulatorAndClose(t *testing.T) {
	isolateOpenEnvironment(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
		t.Error("constructor made RPC")
		return nil, errors.New("unexpected RPC")
	}))
	go server.Serve(listener)
	defer server.Stop()
	defer listener.Close()
	t.Setenv("PUBSUB_EMULATOR_HOST", listener.Addr().String())
	p, consume, closeFn, err := Open(context.Background(), "gcp")
	if err != nil {
		t.Fatal(err)
	}
	if p == nil || consume == nil || closeFn == nil {
		t.Fatal("incomplete runtime")
	}
	closeFn()
	closeFn()
	if err := p.Publish(context.Background(), validNotice(t)); !errors.Is(err, ErrClosed) {
		t.Fatal("Open cleanup did not close runtime", err)
	}
}
