package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/rpc"
)

func cookieFixture() CookiePolicy {
	return CookiePolicy{Type: "access", Types: []string{"access", "refresh"}, Origins: []string{"https://app.example"}}
}
func cookieRequest(method string) *http.Request {
	r := httptest.NewRequest(method, "/commands/login", nil)
	r.AddCookie(&http.Cookie{Name: "__Host-access", Value: "fixture"})
	r.AddCookie(&http.Cookie{Name: "__Host-csrf", Value: "csrf-value"})
	r.Header.Set("Origin", "https://app.example")
	r.Header.Set("X-CSRF-Token", "csrf-value")
	return r
}
func TestGatewayCookieOriginCSRFAndAmbiguity(t *testing.T) {
	policy := cookieFixture()
	auth := Authenticator{Bearer: fixtureVerifier, Cookies: &policy}
	if _, err := auth.Authenticate(cookieRequest("POST")); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"missing-origin", "wrong-origin", "missing-csrf", "wrong-csrf", "duplicate-cookie", "mixed-credentials"} {
		t.Run(mode, func(t *testing.T) {
			r := cookieRequest("POST")
			switch mode {
			case "missing-origin":
				r.Header.Del("Origin")
			case "wrong-origin":
				r.Header.Set("Origin", "https://evil.example")
			case "missing-csrf":
				r.Header.Del("X-CSRF-Token")
			case "wrong-csrf":
				r.Header.Set("X-CSRF-Token", "wrong")
			case "duplicate-cookie":
				r.AddCookie(&http.Cookie{Name: "__Host-access", Value: "other"})
			case "mixed-credentials":
				r.Header.Set("Authorization", "Bearer fixture")
			}
			if _, err := auth.Authenticate(r); err == nil {
				t.Fatal("unsafe cookie request accepted")
			}
		})
	}
	r := cookieRequest("GET")
	r.Header.Del("Origin")
	r.Header.Del("X-CSRF-Token")
	if _, err := auth.Authenticate(r); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	csrf, err := policy.SetCSRF(w)
	if err != nil || len(csrf) < 32 {
		t.Fatal(csrf, err)
	}
	cookie := w.Result().Cookies()[0]
	if cookie.Name != "__Host-csrf" || !cookie.Secure || cookie.HttpOnly || cookie.Domain != "" || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal(cookie)
	}
}
func TestGatewayCookieCommandResponseAndRevocation(t *testing.T) {
	issued, _ := eventstore.Encode(CookieToken{Type: "access", Value: "secret-token", Expires: time.Now().Add(time.Hour).Unix()})
	refreshed, _ := eventstore.Encode(CookieToken{Type: "refresh", Value: "refresh-token", Expires: time.Now().Add(time.Hour).Unix()})
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = write(w, 201, command.Response{Contribution: command.Contribution{Tokens: []json.RawMessage{issued, refreshed}, Revoke: []string{"access"}, Headers: map[string]string{"Set-Cookie": "evil=1"}}, ID: "command", TransactionID: "tx"})
	})
	h, err := CookieGateway(next, cookieFixture(), []string{"/commands/login"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, cookieRequest("POST"))
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 2 {
		t.Fatal(cookies)
	}
	for _, c := range cookies {
		if !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" || c.SameSite != http.SameSiteStrictMode {
			t.Fatal(c)
		}
		if c.Name == "__Host-access" && c.MaxAge != -1 {
			t.Fatal("revocation lost")
		}
	}
	if strings.Contains(w.Body.String(), "secret-token") || strings.Contains(w.Body.String(), "refresh-token") || strings.Contains(w.Body.String(), "evil=1") {
		t.Fatal("token leaked", w.Body.String())
	}
	r := cookieRequest("POST")
	r.Header.Set("Origin", "https://other.example")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 || calls != 1 {
		t.Fatal("command ran before origin check", calls, w.Code)
	}
	// All cookie instructions validate before any cookies can escape.
	next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = write(w, 200, command.Response{Contribution: command.Contribution{Tokens: []json.RawMessage{issued, json.RawMessage(`{"type":"foreign","value":"x","expires":9999999999}`)}}})
	})
	h, _ = CookieGateway(next, cookieFixture(), []string{"/commands/login"}, 1<<20)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, cookieRequest("POST"))
	if w.Code != 500 || len(w.Result().Cookies()) != 0 {
		t.Fatal("partial cookie instructions escaped", w.Code, w.Result().Cookies())
	}
	h, _ = CookieGateway(next, cookieFixture(), []string{"/commands/login"}, 10)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, cookieRequest("POST"))
	if w.Code != 500 {
		t.Fatal("response bound ignored", w.Code)
	}
}
func proxyFixture(t *testing.T, upstream http.Handler) (http.Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	client, err := rpc.New(server.URL, rpc.Options{HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := (FactProxy{Authorize: func(r *http.Request) (Identity, error) {
		return (Authenticator{Bearer: fixtureVerifier}).Authenticate(r)
	}, RPC: client, Path: "/facts/download", Upstream: func(ctx context.Context, id Identity) (http.Header, error) {
		if string(id.Claims) != `{"sub":"trusted"}` {
			return nil, dia.ErrUnauthorized
		}
		return http.Header{"Authorization": {"Bearer peer-only"}}, nil
	}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	return proxy, server
}
func TestGatewayFactProxyForwardsRawHeaders(t *testing.T) {
	raw := []byte{0, 255, 1, 128}
	h, _ := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/facts/download" || r.Header.Get("Authorization") != "Bearer peer-only" || r.Header.Get("Cookie") != "" {
			t.Error("upstream authority not isolated")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Download", "example")
		w.Header().Set("Connection", "X-Internal")
		w.Header().Set("X-Internal", "private")
		w.Header().Set("Content-Length", "4")
		w.Header().Set("Set-Cookie", "foreign=secret")
		_, _ = w.Write(raw)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authHTTPRequest("GET", "/download"))
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), raw) || w.Header().Get("Content-Type") != "application/octet-stream" || w.Header().Get("X-Download") != "example" {
		t.Fatal(w.Code, w.Header(), w.Body.Bytes())
	}
	for _, name := range []string{"Connection", "Content-Length", "X-Internal", "Set-Cookie"} {
		if w.Header().Get(name) != "" {
			t.Fatal("unsafe forwarded header", name)
		}
	}
}
func TestGatewayFactProxyRejectsBeforeStreaming(t *testing.T) {
	h, _ := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Download", "must-not-forward")
		w.WriteHeader(403)
		_, _ = io.WriteString(w, "secret failure")
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authHTTPRequest("GET", "/download"))
	if w.Code != 403 || w.Header().Get("X-Download") != "" || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
}
func TestGatewayFactProxyCancelsUpstreamOnDisconnect(t *testing.T) {
	cancelled := make(chan struct{})
	h, _ := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("first"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		<-r.Context().Done()
		close(cancelled)
	}))
	downstream := httptest.NewServer(h)
	defer downstream.Close()
	req, _ := http.NewRequest("GET", downstream.URL, nil)
	req.Header.Set("Authorization", "Bearer fixture")
	response, err := downstream.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 5)
	if _, err = io.ReadFull(response.Body, first); err != nil || string(first) != "first" {
		t.Fatal(string(first), err)
	}
	response.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("downstream disconnect did not cancel upstream")
	}
}
func TestGatewayFactProxyTruncationIsNotSuccess(t *testing.T) {
	h, _ := proxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("first"))
		_ = http.NewResponseController(w).Flush()
	}))
	server := httptest.NewServer(h)
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("Authorization", "Bearer fixture")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err == nil || !bytes.Equal(raw, []byte("first")) {
		t.Fatal(string(raw), err)
	}
}

func TestGatewayCookieFrameworkComposition(t *testing.T) {
	policy := cookieFixture()
	authority, err := NewGatewayAuthority(Authenticator{Bearer: fixtureVerifier, Cookies: &policy}, map[GatewayRoute]GatewayPolicy{{Method: "POST", Pattern: "/commands/{name}", Name: "login"}: {Check: func(context.Context, Identity, GatewayRequest) error { return nil }}})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := eventstore.Encode(CookieToken{Type: "access", Value: "next-token", Expires: time.Now().Add(time.Hour).Unix()})
	calls := 0
	framework := Framework{Authorize: authority.Framework, Commands: map[string]command.Procedure{"login": {Main: func(ctx context.Context, r command.Request) (command.Result, error) {
		calls++
		if string(r.Claims) != `{"sub":"trusted"}` {
			t.Fatal("forged claims reached command")
		}
		return command.Result{Response: command.Contribution{Tokens: []json.RawMessage{token}}}, nil
	}}}}
	h, err := framework.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h, err = CookieGateway(h, policy, []string{"/commands/login"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"id":"request","issued":"2026-11-12T00:00:00Z","payload":{},"claims":{"sub":"forged"}}`
	r := cookieRequest("POST")
	r.Body = io.NopCloser(strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || calls != 1 || len(w.Result().Cookies()) != 1 || w.Result().Cookies()[0].Value != "next-token" {
		t.Fatal(w.Code, calls, w.Body.String(), w.Result().Cookies())
	}
	r = httptest.NewRequest("OPTIONS", "/commands/login", nil)
	r.Header.Set("Origin", "https://app.example")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "Content-Type, X-CSRF-Token")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Credentials") != "true" || calls != 1 {
		t.Fatal("preflight", w.Code, w.Header(), calls)
	}
}
