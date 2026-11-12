//go:build integration

package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	mongoStore "blossm.local/backend/storage/mongo"
	"blossm.local/backend/storage/postgres"
)

func TestNativeRetainedRouteCoverage(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			namespace := fmt.Sprintf("route_coverage_%d", time.Now().UnixNano())
			var backend eventstore.Backend
			var err error
			if kind == "postgres" {
				if os.Getenv("POSTGRES_URL") == "" {
					t.Fatal("POSTGRES_URL required")
				}
				backend, err = postgres.Open(ctx, os.Getenv("POSTGRES_URL"), namespace)
			} else {
				if os.Getenv("MONGODB_URI") == "" {
					t.Fatal("MONGODB_URI required")
				}
				backend, err = mongoStore.Open(ctx, mongoStore.Config{URI: os.Getenv("MONGODB_URI"), Database: "blossm_test", Namespace: namespace})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close(ctx)
			store := &eventstore.Store{Backend: backend, Reducers: dia.Reducers()}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			app := &dia.Service{Store: store, Network: "local", JWT: &dia.JWT{Signer: dia.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.local", Audience: "local"}, HMACKey: bytes.Repeat([]byte("h"), 32), EncryptionKey: bytes.Repeat([]byte("e"), 32)}
			server := httptest.NewServer(API{Dia: app, Store: store}.Handler())
			defer server.Close()
			request := func(path, token, key string, input any, status int) (http.Header, []byte) {
				t.Helper()
				body, e := json.Marshal(input)
				if e != nil {
					t.Fatal(e)
				}
				req, e := http.NewRequest("POST", server.URL+path, bytes.NewReader(body))
				if e != nil {
					t.Fatal(e)
				}
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Idempotency-Key", key)
				resp, e := server.Client().Do(req)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				out, e := io.ReadAll(resp.Body)
				if e != nil {
					t.Fatal(e)
				}
				if resp.StatusCode != status {
					t.Fatalf("%s: status %d want %d: %s", path, resp.StatusCode, status, out)
				}
				if status != 200 && status != 201 && resp.Header.Get("Grip-Channel") != "" {
					t.Fatal("unauthorized GRIP channel leak")
				}
				return resp.Header, out
			}
			decode := func(raw []byte, out any) {
				t.Helper()
				if e := json.Unmarshal(raw, out); e != nil {
					t.Fatal(e)
				}
			}
			makeNode := func(id string) dia.NodeResult {
				t.Helper()
				secret, e := dia.NewSessionSecret()
				if e != nil {
					t.Fatal(e)
				}
				_, raw := request("/commands/session/start", "", secret, map[string]any{"device": dia.Device{Type: "desktop", Platform: "browser", Agent: "fixture"}}, 201)
				var start dia.SessionResult
				decode(raw, &start)
				_, raw = request("/commands/node/register", start.Token, id, dia.NodeInput{Network: id + ".test"}, 200)
				var node dia.NodeResult
				decode(raw, &node)
				return node
			}
			node := makeNode("owner")
			other := makeNode("other")
			groupInput := map[string]any{"group": node.Group}
			headers, raw := request("/facts/group/principals/stream", node.Token, "", groupInput, 200)
			if headers.Get("Content-Type") != "application/x-ndjson" || headers.Get("Cache-Control") != "no-store" {
				t.Fatal("wrong stream headers", headers)
			}
			var streamed []dia.Ref
			decoder := json.NewDecoder(bytes.NewReader(raw))
			for {
				var member dia.Ref
				e := decoder.Decode(&member)
				if e == io.EOF {
					break
				}
				if e != nil {
					t.Fatal(e)
				}
				streamed = append(streamed, member)
			}
			_, raw = request("/facts/group/principals", node.Token, "", groupInput, 200)
			var listed []dia.Ref
			decode(raw, &listed)
			a, _ := json.Marshal(streamed)
			b, _ := json.Marshal(listed)
			if len(streamed) == 0 || !bytes.Equal(a, b) {
				t.Fatalf("stream/list mismatch: %s %s", a, b)
			}
			request("/facts/group/principals/stream", other.Token, "", groupInput, 403)
			request("/facts/group/principals/stream", "", "", groupInput, 401)
			// Get a connection-updates token entirely through mounted command routes.
			_, raw = request("/commands/key/create", node.Token, "key", dia.KeyInput{Node: node.Node, Name: "fixture", Roles: []string{"NodeNetwork"}}, 200)
			var apiKey dia.KeyResult
			decode(raw, &apiKey)
			_, raw = request("/commands/connection/open", "", "", dia.ConnectionInput{Key: apiKey.Key, Secret: apiKey.Secret}, 200)
			var connection struct{ Token string }
			decode(raw, &connection)
			_, raw = request("/commands/updates/create-token", connection.Token, "", map[string]string{"kind": "connection"}, 200)
			var updates string
			decode(raw, &updates)
			channelInput := map[string]any{"kind": "connection-updates", "channel": dia.ChannelInput{Name: "sessions", Key: "device"}}
			_, raw = request("/facts/updates/channel", updates, "", channelInput, 200)
			var channel string
			decode(raw, &channel)
			headers, raw = request("/facts/updates/channel/stream", updates, "", channelInput, 200)
			if headers.Get("Content-Type") != "text/event-stream" || headers.Get("Grip-Channel") != channel || headers.Get("Grip-Hold") != "stream" || headers.Get("Grip-Keep-Alive") != `:\n\n; format=cstring; timeout=20` || string(raw) != ":"+strings.Repeat(" ", 2047)+"\n\n" {
				t.Fatalf("invalid GRIP handshake: %v", headers)
			}
			request("/facts/updates/channel/stream", connection.Token, "", channelInput, 401)
			request("/facts/updates/channel/stream", node.Token, "", channelInput, 401)
			request("/facts/updates/channel/stream", "", "", channelInput, 401)
			request("/facts/updates/channel/stream", updates, "", map[string]any{"kind": "updates", "channel": dia.ChannelInput{Name: "sessions"}}, 401)
			request("/facts/updates/channel/stream", updates, "", map[string]any{"kind": "connection-updates", "channel": map[string]any{"name": "sessions", "subject": other.Node}}, 400)
			request("/facts/updates/channel/stream", updates, "", map[string]any{"kind": "connection-updates", "channel": dia.ChannelInput{Name: "sessions\r\nGrip-Channel: victim"}}, 400)
			// Identical channel inputs under a different authorized node cannot join the first subject's channel.
			_, raw = request("/commands/key/create", other.Token, "other-key", dia.KeyInput{Node: other.Node, Name: "other", Roles: []string{"NodeNetwork"}}, 200)
			var otherKey dia.KeyResult
			decode(raw, &otherKey)
			_, raw = request("/commands/connection/open", "", "", dia.ConnectionInput{Key: otherKey.Key, Secret: otherKey.Secret}, 200)
			decode(raw, &connection)
			_, raw = request("/commands/updates/create-token", connection.Token, "", map[string]string{"kind": "connection"}, 200)
			var otherUpdates string
			decode(raw, &otherUpdates)
			headers, _ = request("/facts/updates/channel/stream", otherUpdates, "", channelInput, 200)
			if headers.Get("Grip-Channel") == channel || headers.Get("Grip-Channel") == "" {
				t.Fatal("cross-subject channel collision")
			}
			request("/commands/key/revoke", node.Token, "revoke", map[string]any{"key": apiKey.Key}, 200)
			request("/facts/updates/channel/stream", updates, "", channelInput, 401)
			// Subject removal must affect only the authorized subject and revoke group reads.
			remove := map[string]any{"principal": node.Principal, "subjects": []dia.Ref{node.Group}}
			request("/commands/principal/remove-roles", other.Token, "forbidden-removal", remove, 403)
			request("/commands/principal/remove-roles", node.Token, "remove-group", remove, 200)
			request("/facts/group/principals/stream", node.Token, "", groupInput, 403)
			_, raw = request("/facts/principal/roles", node.Token, "", map[string]any{}, 200)
			var roles []dia.RoleGrant
			decode(raw, &roles)
			foundNode := false
			for _, role := range roles {
				if role.Subject == node.Group {
					t.Fatal("subject roles survived")
				}
				if role.Subject == node.Node {
					foundNode = true
				}
			}
			if !foundNode {
				t.Fatal("unrelated subject roles removed")
			}
		})
	}
}
