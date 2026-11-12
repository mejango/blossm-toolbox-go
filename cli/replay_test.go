package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayRequiresLogicalRunAndValidatedAcknowledgment(t *testing.T) {
	config := configFile(t, "gcp")
	file := filepath.Join(t.TempDir(), "replay.json")
	args := []string{"replay", "--config", config, "--url", "https://example.invalid/projections/documents/replay", "--input", file, "--idempotency-key", "retry-1"}
	var out bytes.Buffer
	for _, payload := range []string{`{}`, `{"limit":10}`, `{"runId":" ","limit":10}`, `{"runId":"run","limit":0}`, `{"runId":"run","runId":"other","limit":10}`} {
		if err := os.WriteFile(file, []byte(payload), 0600); err != nil {
			t.Fatal(err)
		}
		e := Execution{Getenv: func(string) string { t.Fatal("read credentials before validation"); return "" }}
		if err := RunWith(context.Background(), args, &out, &out, e); err == nil {
			t.Fatal("accepted invalid run", payload)
		}
	}
	if err := os.WriteFile(file, []byte(`{"runId":"run","limit":10}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, ack := range []string{`null`, `{}`, `{"runId":"other","enqueued":0,"after":"","complete":true}`, `{"runId":"run","enqueued":0,"after":"","complete":true}`} {
		calls := 0
		e := Execution{Getenv: func(string) string { return "token" }, HTTP: &http.Client{Transport: workflowTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(ack))}, nil
		})}}
		err := RunWith(context.Background(), append(args, "--execute"), &out, &out, e)
		valid := strings.Contains(ack, `"runId":"run"`)
		if (err == nil) != valid || calls != 1 {
			t.Fatal("wrong ack result", ack, err, calls)
		}
	}
}
func TestRejectStaticReplayConfiguration(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "deploy", "examples", "gcp-framework-postgres.json"))
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.Replace(data, []byte(`"delivery": {`), []byte(`"delivery": {"replayRunID":"static",`), 1)
	if _, err := DecodeConfig(bytes.NewReader(data)); err == nil {
		t.Fatal("static replay config accepted")
	}
	c := Example("gcp", "postgres")
	c.Secrets["REPLAY_RUN_ID"] = Secret{Reference: "static", Version: "1"}
	if err := c.Validate(); err == nil {
		t.Fatal("obsolete replay environment accepted as secret")
	}
}

func TestOperationURLLoopbackBoundaryBeforeCredentials(t *testing.T) {
	config := configFile(t, "gcp")
	input := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(input, []byte(`{"runId":"local-run","limit":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://localhost:8080", "http://127.0.0.1:8080", "http://127.0.2.1:8080", "http://[::1]:8080", "http://[::ffff:127.0.0.1]:8080", "https://remote.example"} {
		var out bytes.Buffer
		e := Execution{Getenv: func(string) string { t.Fatal("offline URL validation read secret"); return "" }, HTTP: &http.Client{Transport: workflowTransport(func(*http.Request) (*http.Response, error) {
			t.Fatal("offline validation sent request")
			return nil, nil
		})}}
		if err := RunWith(context.Background(), []string{"replay", "--config", config, "--url", endpoint + "/projections/documents/replay", "--input", input}, &out, &out, e); err != nil {
			t.Fatal(endpoint, err)
		}
	}
	for _, endpoint := range []string{"http://remote.example/path", "http://localhost.evil/path", "http://0.0.0.0/path", "http://192.168.1.2/path", "http://[::2]/path", "http://user:secret@localhost/path", "http://localhost/path?x=1", "http://localhost/path?", "http://localhost/path#", "http://localhost:99999/path", "ftp://localhost/path"} {
		var out bytes.Buffer
		e := Execution{Getenv: func(string) string { t.Fatal("unsafe endpoint reached secret access"); return "" }}
		if err := RunWith(context.Background(), []string{"job", "--config", config, "--url", endpoint, "--execute"}, &out, &out, e); err == nil {
			t.Fatal("accepted unsafe endpoint", endpoint)
		}
	}
}
