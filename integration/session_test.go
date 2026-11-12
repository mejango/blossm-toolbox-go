//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"blossm.local/backend/dia"
	"blossm.local/backend/transport"
)

func TestDiaSessionFlow(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			store := newStore(t, kind)
			store.Reducers = dia.Reducers()
			key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if e != nil {
				t.Fatal(e)
			}
			app := &dia.Service{Store: store, Network: "local", JWT: &dia.JWT{Signer: dia.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.local", Audience: "local"}}
			server := httptest.NewServer(transport.API{Dia: app, Store: store}.Handler())
			defer server.Close()
			secret, e := dia.NewSessionSecret()
			if e != nil {
				t.Fatal(e)
			}
			req, _ := http.NewRequest("POST", server.URL+"/commands/session/start", bytes.NewBufferString(`{"device":{"type":"desktop","platform":"browser","agent":"fixture"}}`))
			req.Header.Set("Idempotency-Key", secret)
			response, e := server.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			defer response.Body.Close()
			if response.StatusCode != 201 {
				_, detail := app.StartSession(context.Background(), secret, dia.Device{Type: "desktop", Platform: "browser", Agent: "fixture", IP: "127.0.0.1"})
				t.Fatalf("start status%d error=%v", response.StatusCode, detail)
			}
			var result dia.SessionResult
			if e = json.NewDecoder(response.Body).Decode(&result); e != nil {
				t.Fatal(e)
			}
			if result.Token == "" {
				t.Fatal("missing signed token")
			}
			if e = store.Deliver(context.Background(), transport.SessionProjection{Store: store}, 100); e != nil {
				t.Fatal(e)
			}
			req, _ = http.NewRequest("GET", server.URL+"/views/session", nil)
			req.Header.Set("Authorization", "Bearer "+result.Token)
			response, e = server.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("protected view status%d", response.StatusCode)
			}
			response, e = server.Client().Get(server.URL + "/views/session")
			if e != nil {
				t.Fatal(e)
			}
			response.Body.Close()
			if response.StatusCode != 401 {
				t.Fatalf("public device leak status%d", response.StatusCode)
			}
			req, _ = http.NewRequest("POST", server.URL+"/commands/session/terminate", nil)
			req.Header.Set("Authorization", "Bearer "+result.Token)
			req.Header.Set("Idempotency-Key", "terminate-fixture")
			response, e = server.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			response.Body.Close()
			if response.StatusCode != 200 {
				t.Fatalf("terminate status%d", response.StatusCode)
			}
			req, _ = http.NewRequest("GET", server.URL+"/views/session", nil)
			req.Header.Set("Authorization", "Bearer "+result.Token)
			response, e = server.Client().Do(req)
			if e != nil {
				t.Fatal(e)
			}
			response.Body.Close()
			if response.StatusCode != 401 {
				t.Fatalf("revocation failed status%d", response.StatusCode)
			}
		})
	}
}
