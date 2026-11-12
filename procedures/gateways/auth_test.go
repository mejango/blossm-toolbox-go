package transport

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/lib/authentication_error"
	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/lib/jwt"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/job"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

func authIdentity() Identity {
	return Identity{Context: command.Context{"principal": json.RawMessage(`{"network":"n","service":"core","domain":"principal","id":"p"}`)}, Claims: json.RawMessage(`{"sub":"trusted"}`)}
}
func fixtureVerifier(_ context.Context, token string) (Identity, error) {
	if token != "fixture" {
		return Identity{}, errors.New("bad credential")
	}
	return authIdentity(), nil
}
func authHTTPRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Authorization", "Bearer fixture")
	return r
}

func TestGatewayCredentialModesAndIsolation(t *testing.T) {
	shared := authIdentity()
	a := Authenticator{Bearer: func(context.Context, string) (Identity, error) { return shared, nil }, Basic: func(_ context.Context, user, password string) (Identity, error) {
		if user != "key" || password != "secret:part" {
			return Identity{}, authn.ErrUnauthorized
		}
		return authIdentity(), nil
	}, Key: fixtureVerifier}
	r := authHTTPRequest("GET", "/")
	id, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	id.Context["principal"][0] = 'x'
	id.Claims[0] = 'x'
	if shared.Context["principal"][0] != '{' || shared.Claims[0] != '{' {
		t.Fatal("verifier identity mutated")
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.SetBasicAuth("key", "secret:part")
	if _, err = a.Authenticate(r); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-API-Key", "fixture")
	if _, err = a.Authenticate(r); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer fixture")
	if _, err = a.Authenticate(r); err == nil {
		t.Fatal("ambiguous credentials")
	}
	for _, header := range []string{"", "Bearer", "Bearer ", "Basic broken", "Basic OmJhZA==", "Other fixture"} {
		r = httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", header)
		if _, err = a.Authenticate(r); err == nil {
			t.Fatal("invalid scheme", header)
		}
	}
	optional := Authenticator{Bearer: fixtureVerifier, Optional: true}
	r = httptest.NewRequest("GET", "/", nil)
	if _, err = optional.Authenticate(r); err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer invalid")
	if _, err = optional.Authenticate(r); err == nil {
		t.Fatal("invalid credential downgraded to anonymous")
	}
}
func TestGatewayExactRouteJobAndViewAuthority(t *testing.T) {
	principal := eventlib.Root{Network: "n", Service: "core", Domain: "principal", ID: "p"}
	check := func(_ context.Context, id Identity, in GatewayRequest) error {
		if in.Name != "allowed" {
			return authn.ErrForbidden
		}
		return nil
	}
	routes := map[GatewayRoute]GatewayPolicy{
		{Method: "POST", Pattern: "/jobs/{name}", Name: "allowed"}:                                   {Check: check, RequiredContexts: []string{"principal"}, AllowedContexts: map[string][]eventlib.Root{"principal": {principal}}},
		{Method: "GET", Pattern: "/view-composites/{name}", Name: "allowed", Operation: "composite"}: {Check: check, Scope: func(context.Context, Identity, GatewayRequest) (view.Scope, error) { return gatewayScope(), nil }},
		{Method: "POST", Pattern: "/projections/{name}/play", Name: "allowed", Operation: "play"}: {Check: func(_ context.Context, _ Identity, in GatewayRequest) error {
			if in.RootID != "owned" {
				return authn.ErrForbidden
			}
			return nil
		}, Projection: func(context.Context, Identity, GatewayRequest) (ProjectionBinding, error) {
			return ProjectionBinding{}, nil
		}},
	}
	authority, err := NewGatewayAuthority(Authenticator{Bearer: fixtureVerifier}, routes)
	if err != nil {
		t.Fatal(err)
	}
	delete(routes, GatewayRoute{Method: "POST", Pattern: "/jobs/{name}", Name: "allowed"})
	ran := 0
	f := Framework{Authorize: authority.Framework, Jobs: map[string]job.Procedure{"allowed": {Run: func(ctx context.Context, in job.Input) error {
		ran++
		if string(in.Claims) != `{"sub":"trusted"}` || strings.Contains(string(in.Context), "forged") {
			t.Fatal("untrusted job identity", string(in.Claims), string(in.Context))
		}
		return nil
	}}, "other": {Run: func(context.Context, job.Input) error { t.Fatal("unregistered job ran"); return nil }}}}
	h, err := f.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"allowed", "other"} {
		r := httptest.NewRequest("POST", "/jobs/"+name, strings.NewReader(`{"payload":{},"claims":{"sub":"forged"},"context":{"principal":"forged"}}`))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if name == "allowed" && w.Code != 204 || name == "other" && w.Code < 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if ran != 1 {
		t.Fatal(ran)
	}
	views := ViewGateway{Authorize: authority.View, Composites: map[string]ViewComposite{"allowed": TypedViewComposite[struct{}, map[string]string](nil, func(ctx context.Context, _ struct{}, s view.Scope, _ *view.Components) (map[string]string, error) {
		return map[string]string{"owner": string(s.Owner[0].Value)}, nil
	}, nil)}}
	h, err = views.Handler()
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authHTTPRequest("GET", "/view-composites/allowed"))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "alice") {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, authHTTPRequest("GET", "/view-composites/allowed/stream"))
	if w.Code < 400 {
		t.Fatal("unregistered operation allowed")
	}
	r := authHTTPRequest("POST", "/projections/allowed/play")
	r.Pattern = "POST /projections/{name}/play"
	r.SetPathValue("name", "allowed")
	if _, err = authority.Projection(r, ProjectionOperation{Name: "allowed", RootID: "foreign"}); !errors.Is(err, authn.ErrForbidden) {
		t.Fatal(err)
	}
	if _, err = authority.Projection(r, ProjectionOperation{Name: "allowed", RootID: "owned"}); err != nil {
		t.Fatal(err)
	}
	if _, err = authority.Projection(r, ProjectionOperation{Name: "allowed", Replay: true}); !errors.Is(err, authn.ErrForbidden) {
		t.Fatal("play grant became replay", err)
	}
}
func TestGatewayPinnedServiceJWT(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwt := token.JWT[command.Context]{Signer: token.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "issuer", Audience: "n"}
	claims := token.Claims[command.Context]{Issuer: "issuer", Audience: []string{"n"}, Subject: "p", ID: "jti", Kind: "access", NotBefore: time.Now().Unix() - 1, Expires: time.Now().Unix() + 600, Context: command.Context{}}
	claims.Kind = "service"
	claims.Subject = "peer"
	verify, err := ServiceJWTVerifier(jwt, "peer")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"service", "access"} {
		claims.Kind = kind
		token, err := jwt.Sign(context.Background(), claims)
		if err != nil {
			t.Fatal(err)
		}
		_, err = verify(context.Background(), token)
		if (err == nil) != (kind == "service") {
			t.Fatal(kind, err)
		}
	}
	claims.Kind = "service"
	claims.Subject = "other"
	token, _ := jwt.Sign(context.Background(), claims)
	if _, err = verify(context.Background(), token); err == nil {
		t.Fatal("wrong peer")
	}
}

func TestGatewayBodyResourceAuthorizationPreservesExecutionInput(t *testing.T) {
	owned := eventlib.Root{Network: "n", Service: "core", Domain: "principal", ID: "p"}
	authority, err := NewGatewayAuthority(Authenticator{Bearer: fixtureVerifier}, map[GatewayRoute]GatewayPolicy{{Method: "POST", Pattern: "/inspect"}: {Check: func(ctx context.Context, id Identity, in GatewayRequest) error {
		var root eventlib.Root
		if err := InspectGatewayJSON(in.Request, &root); err != nil {
			return err
		}
		var principal eventlib.Root
		if err := canonicaljson.Decode(id.Context["principal"], &principal); err != nil {
			return authn.ErrForbidden
		}
		if root != principal {
			return authn.ErrForbidden
		}
		return nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /inspect", Wrap(func(w http.ResponseWriter, r *http.Request) error {
		if _, err := authority.Framework(r); err != nil {
			return err
		}
		var root eventlib.Root
		if err := Decode(w, r, &root); err != nil {
			return err
		}
		if root != owned {
			t.Fatal("authorized body changed")
		}
		calls++
		return Write(w, 200, root)
	}))
	for _, id := range []string{"p", "foreign"} {
		root := owned
		root.ID = id
		raw, _ := canonicaljson.Encode(root)
		r := httptest.NewRequest("POST", "/inspect", bytes.NewReader(raw))
		r.Header.Set("Authorization", "Bearer fixture")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if id == "p" && w.Code != 200 || id != "p" && w.Code != 403 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
}

func TestGatewayProtectedContextsAndMissingRegistrations(t *testing.T) {
	expected := eventlib.Root{Network: "n", Service: "core", Domain: "principal", ID: "p"}
	refs := []eventlib.Root{expected}
	identity := authIdentity()
	auth := Authenticator{Bearer: func(context.Context, string) (Identity, error) { return identity, nil }}
	route := GatewayRoute{Method: "POST", Pattern: "/commands/{name}", Name: "run"}
	authority, err := NewGatewayAuthority(auth, map[GatewayRoute]GatewayPolicy{route: {Check: func(context.Context, Identity, GatewayRequest) error { return nil }, RequiredContexts: []string{"principal"}, AllowedContexts: map[string][]eventlib.Root{"principal": refs}}})
	if err != nil {
		t.Fatal(err)
	}
	refs[0].ID = "mutated-config"
	request := func(method, name string) *http.Request {
		r := authHTTPRequest(method, "/commands/"+name)
		r.Pattern = "POST /commands/{name}"
		r.SetPathValue("name", name)
		return r
	}
	if _, err = authority.Framework(request("POST", "run")); err != nil {
		t.Fatal(err)
	}
	for _, contextJSON := range []string{`null`, `{"network":"n","service":"core","domain":"principal","id":"other"}`, `{"network":"foreign","service":"core","domain":"principal","id":"p"}`} {
		identity.Context["principal"] = json.RawMessage(contextJSON)
		if _, err = authority.Framework(request("POST", "run")); !errors.Is(err, authn.ErrForbidden) {
			t.Fatal("unprotected context", contextJSON, err)
		}
	}
	identity = authIdentity()
	for _, r := range []*http.Request{request("GET", "run"), request("POST", "unregistered")} {
		if _, err = authority.Framework(r); !errors.Is(err, authn.ErrForbidden) {
			t.Fatal("unregistered route", err)
		}
	}
	if _, err = NewGatewayAuthority(auth, map[GatewayRoute]GatewayPolicy{route: {}}); err == nil {
		t.Fatal("missing check accepted")
	}
}
