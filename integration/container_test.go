//go:build containerintegration

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// This gate exercises the shipped Linux executable, not an in-process handler.
func containerFixture(t *testing.T, framework bool) (context.Context, string, *http.Client, func(string, string, string, string, int) []byte) {
	t.Helper()
	image, network, uri := os.Getenv("CONTAINER_IMAGE"), os.Getenv("CONTAINER_NETWORK"), os.Getenv("CONTAINER_POSTGRES_URL")
	if image == "" || network == "" || uri == "" {
		t.Fatal("CONTAINER_IMAGE, CONTAINER_NETWORK and CONTAINER_POSTGRES_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	secret := func() string {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		return base64.StdEncoding.EncodeToString(b)
	}
	name := fmt.Sprintf("blossm-go-smoke-%d", time.Now().UnixNano())
	env := append(os.Environ(), "JWT_PRIVATE_KEY="+string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "DIA_HMAC_KEY="+secret(), "DIA_ENCRYPTION_KEY="+secret())
	docker := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "docker", args...)
		cmd.Env = env
		return cmd.CombinedOutput()
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(cleanup, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
			t.Errorf("container cleanup: %v %s", err, out)
		}
	})
	args := []string{"run", "--detach", "--platform", "linux/amd64", "--name", name, "--network", network, "--publish", "127.0.0.1::8080", "--env", "JWT_SIGNER=local", "--env", "JWT_PRIVATE_KEY", "--env", "DIA_HMAC_KEY", "--env", "DIA_ENCRYPTION_KEY", "--env", "NETWORK=container-test", "--env", "LOCAL_MODE=true", "--env", "DATABASE_DRIVER=postgres", "--env", "DATABASE_NAMESPACE=" + name, "--env", "POSTGRES_URL=" + uri, image}
	if framework {
		args = args[:len(args)-1]
		args = append(args, "--entrypoint", "/framework", "--env", "SERVICE=reference", "--env", "PUBLICATION_MODE=inline", "--env", "INTERNAL_SUBJECT=container-worker", "--env", "IDENTITY_DATABASE_NAMESPACE="+name+"_identity", image)
	}
	if out, err := docker(args...); err != nil {
		t.Fatalf("container start: %v %s", err, out)
	}
	port, err := docker("port", name, "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + strings.TrimSpace(string(port))
	client := &http.Client{Timeout: 10 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	ready := false
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get(base + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	if !ready {
		t.Fatal("container never became healthy")
	}
	request := func(path, body, token, key string, want int) []byte {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			t.Fatalf("%s status %d, want %d", path, response.StatusCode, want)
		}
		return raw
	}
	return ctx, base, client, request
}

func TestContainerSessionLifecycle(t *testing.T) {
	_, _, _, request := containerFixture(t, false)
	var err error
	keyBytes := make([]byte, 32)
	if _, err = rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	sessionKey := base64.RawURLEncoding.EncodeToString(keyBytes)
	device := `{"device":{"type":"desktop","platform":"browser","agent":"container-fixture"}}`
	var session struct {
		Token   string          `json:"token"`
		Context json.RawMessage `json:"context"`
	}
	first := request("/commands/session/start", device, "", sessionKey, 201)
	if err = json.Unmarshal(first, &session); err != nil || session.Token == "" || len(session.Context) == 0 || string(session.Context) == "null" {
		t.Fatal("missing session token", err)
	}
	retry := request("/commands/session/start", device, "", sessionKey, 201)
	var again struct {
		Context json.RawMessage `json:"context"`
	}
	if err = json.Unmarshal(retry, &again); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(session.Context, again.Context) {
		t.Fatal("creation retry changed session identity")
	}
	request("/facts/session/terminated", `{}`, session.Token, "", 200)
	request("/commands/session/terminate", `{}`, session.Token, "terminate", 200)
	request("/commands/session/change-scene", `{}`, session.Token, "after-termination", 401)
}

func TestContainerGenericFrameworkLifecycle(t *testing.T) {
	ctx, base, client, request := containerFixture(t, true)
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	var session struct {
		Token string `json:"token"`
	}
	first := request("/identity/commands/session/start", `{"device":{"type":"desktop","platform":"browser","agent":"generic-container"}}`, "", base64.RawURLEncoding.EncodeToString(keyBytes), 201)
	if err := json.Unmarshal(first, &session); err != nil || session.Token == "" {
		t.Fatal("session", err)
	}
	scene := request("/identity/commands/scene/register", `{"subject":{"network":"container-test","service":"system","domain":"node","id":"documents"}}`, session.Token, "scene", 200)
	if err := json.Unmarshal(scene, &session); err != nil || session.Token == "" {
		t.Fatal("scene", err)
	}
	input := `{"id":"container-command","issued":"2026-11-12T00:00:00Z","root":{"network":"container-test","service":"reference","domain":"document","id":"one"},"payload":{"text":"container flow"},"transaction":{"id":"container-command","issued":"2026-11-12T00:00:00Z","path":[]}}`
	for i, want := range []string{"COMMITTED", "DUPLICATE"} {
		raw := request("/commands/set", input, session.Token, "", 200)
		var out struct {
			Receipts []struct {
				Outcome string `json:"outcome"`
			} `json:"receipts"`
		}
		if err := json.Unmarshal(raw, &out); err != nil || len(out.Receipts) != 1 || out.Receipts[0].Outcome != want {
			t.Fatalf("commit/retry %d %s", i, raw)
		}
	}
	read := func(want int) {
		t.Helper()
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/view-stores/documents/one", nil)
		req.Header.Set("Authorization", "Bearer "+session.Token)
		r, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		if r.StatusCode != want {
			t.Fatalf("protected view status %d want%d", r.StatusCode, want)
		}
		if want == 200 {
			var row struct {
				Body struct {
					Text string `json:"text"`
				} `json:"body"`
			}
			if err = json.NewDecoder(r.Body).Decode(&row); err != nil || row.Body.Text != "container flow" {
				t.Fatal("projection", err)
			}
		}
	}
	read(200)
	request("/identity/commands/session/terminate", `{}`, session.Token, "terminate", 200)
	read(401)
}
