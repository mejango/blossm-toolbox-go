package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"blossm.local/backend/command"
	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/rpc"
)

func delegatedFixture(t *testing.T) (dia.JWT, *dia.Service, *authState, command.Request) {
	t.Helper()
	jwt, claims := gatewayJWT(t)
	jwt.Audience = "n/child"
	claims.Expires = time.Now().Unix() + 30
	state := &authState{rows: map[eventstore.Root]eventstore.Aggregate{}}
	state.put(claims.Context.Session, dia.SessionState{Expires: claims.Expires, Principal: claims.Context.Principal})
	service := &dia.Service{JWT: &jwt, Store: state, Network: "n"}
	id, err := identityFromClaims(claims)
	if err != nil {
		t.Fatal(err)
	}
	return jwt, service, state, command.Request{ID: "parent", Issued: "2026-11-12T00:00:00Z", Context: id.Context, Claims: id.Claims, Transaction: eventstore.Transaction{ID: "transaction", Issued: "2026-11-12T00:00:00Z", Path: []eventstore.Trace{{Network: "n", Host: "parent", Procedure: "command", Hash: "v1"}}}}
}

func TestDelegatedCommandRealCredentialComposition(t *testing.T) {
	ctx := context.Background()
	jwt, service, _, parent := delegatedFixture(t)
	mint, err := DelegatedCommandAuthorizer(jwt, service, "n/parent")
	if err != nil {
		t.Fatal(err)
	}
	verify, err := DelegatedCommandGateway(jwt, service, "n/parent")
	if err != nil {
		t.Fatal(err)
	}
	captured := make(chan command.Request, 1)
	child := Framework{Authorize: verify, Commands: map[string]command.Procedure{"reserve": {Trace: &eventstore.Trace{Network: "n", Host: "child", Procedure: "command", Hash: "v2"}, Main: func(_ context.Context, in command.Request) (command.Result, error) {
		captured <- in
		return command.Result{Response: command.Contribution{StatusCode: 204}}, nil
	}}}}
	handler, err := child.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	wire, err := rpc.New(server.URL, rpc.Options{HTTP: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	client := CommandClient{RPC: wire, Target: "n/child", Authorize: mint}
	// The parent Framework authenticates a real access token and its Main invokes
	// the real minter, rather than injecting an invented static delegated header.
	userJWT := jwt
	userJWT.Audience = "n"
	var original dia.Claims
	if err = eventstore.Decode(parent.Claims, &original); err != nil {
		t.Fatal(err)
	}
	userToken, err := userJWT.Sign(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	userService := *service
	userService.JWT = &userJWT
	userVerifier, err := DiaVerifier(&userService, "access")
	if err != nil {
		t.Fatal(err)
	}
	auth := Authenticator{Bearer: userVerifier}
	outer := Framework{Authorize: auth.Authenticate, Commands: map[string]command.Procedure{"copy": {Main: func(ctx context.Context, in command.Request) (command.Result, error) {
		_, err := client.Execute(ctx, in, "reserve", "item", command.Request{Context: command.Context{"principal": json.RawMessage(`"forged"`)}, Claims: json.RawMessage(`{"sub":"forged"}`)})
		return command.Result{Response: command.Contribution{StatusCode: 204}}, err
	}}}}
	outerHandler, err := outer.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := eventstore.Encode(parent)
	request := httptest.NewRequest("POST", "/commands/copy", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+userToken)
	response := httptest.NewRecorder()
	outerHandler.ServeHTTP(response, request)
	if response.Code != 204 {
		t.Fatal(response.Code, response.Body.String())
	}
	in := <-captured
	var delegated dia.Claims
	if err = eventstore.Decode(in.Claims, &delegated); err != nil {
		t.Fatal(err)
	}
	if delegated.Subject != "n/parent" || delegated.Context != original.Context || delegated.Expires != original.Expires || len(delegated.Audience) != 1 || delegated.Audience[0] != "n/child" {
		t.Fatal("lost user or target/expiry authority", delegated)
	}
	if in.Transaction.ID != parent.Transaction.ID || len(in.Transaction.Path) != 2 || in.Transaction.Path[0].Host != "parent" || in.Transaction.Path[1].Host != "child" {
		t.Fatal("lost parent trace", in.Transaction)
	}
	if !bytes.Equal(in.Context["principal"], parent.Context["principal"]) {
		t.Fatal("body identity overrode signed context")
	}
}

func TestDelegatedCommandRejectsTamperedAuthority(t *testing.T) {
	ctx := context.Background()
	jwt, service, state, parent := delegatedFixture(t)
	mint, _ := DelegatedCommandAuthorizer(jwt, service, "n/parent")
	headers, err := mint(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(headers.Get("Authorization"), "Bearer ")
	verify, _ := DelegatedCommandVerifier(jwt, service, "n/parent")
	id, err := verify(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"valid", "id", "issued", "path"} {
		t.Run(field, func(t *testing.T) {
			in := parent
			in.Transaction = eventstore.CloneEvent(eventstore.Event{Input: eventstore.Input{Transaction: parent.Transaction}}).Input.Transaction
			switch field {
			case "id":
				in.Transaction.ID = "other"
			case "issued":
				in.Transaction.Issued = "2026-11-13T00:00:00Z"
			case "path":
				in.Transaction.Path[0].Host = "forged"
			}
			body, _ := eventstore.Encode(in)
			r := httptest.NewRequest("POST", "/commands/reserve", bytes.NewReader(body))
			r.Pattern = "POST /commands/{name}"
			r.SetPathValue("name", "reserve")
			err := DelegatedCommandCheck(ctx, id, GatewayRequest{Request: r})
			if (err == nil) != (field == "valid") {
				t.Fatal(field, err)
			}
			if field == "valid" {
				var again command.Request
				if err = json.NewDecoder(r.Body).Decode(&again); err != nil || again.Transaction.ID != in.Transaction.ID {
					t.Fatal("policy consumed body", err)
				}
			}
		})
	}
	for _, field := range []string{"peer", "audience", "multiple-audiences", "kind", "jti"} {
		t.Run(field, func(t *testing.T) {
			var c dia.Claims
			_ = eventstore.Decode(id.Claims, &c)
			switch field {
			case "peer":
				c.Subject = "other"
			case "audience":
				c.Audience = []string{"n/other"}
			case "multiple-audiences":
				c.Audience = append(c.Audience, "n/other")
			case "kind":
				c.Kind = "access"
			case "jti":
				c.ID = "generic-peer-token"
			}
			signer := jwt
			signer.Audience = c.Audience[0]
			bad, err := signer.Sign(ctx, c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = verify(ctx, bad); err == nil {
				t.Fatal("accepted", field)
			}
		})
	}
	mismatch := parent
	mismatch.Context = command.Context{"principal": json.RawMessage(`"forged"`)}
	if _, err = mint(ctx, mismatch); err == nil {
		t.Fatal("minted substituted context")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = mint(cancelled, parent); err == nil {
		t.Fatal("minted after cancellation")
	}
	var original dia.Claims
	_ = eventstore.Decode(parent.Claims, &original)
	state.put(original.Context.Session, dia.SessionState{Expires: original.Expires, Principal: original.Context.Principal, Terminated: "revoked"})
	if _, err = mint(ctx, parent); err == nil {
		t.Fatal("minted revoked session")
	}
	if _, err = verify(ctx, token); err == nil {
		t.Fatal("verified revoked delegated session")
	}
}

func TestDelegatedCommandExpiryCeiling(t *testing.T) {
	jwt, service, state, parent := delegatedFixture(t)
	var claims dia.Claims
	_ = eventstore.Decode(parent.Claims, &claims)
	now := time.Now()
	jwt.Now = func() time.Time { return now }
	claims.Expires = now.Add(time.Hour).Unix()
	parent.Claims, _ = eventstore.Encode(claims)
	state.put(claims.Context.Session, dia.SessionState{Expires: claims.Expires, Principal: claims.Context.Principal})
	mint, _ := DelegatedCommandAuthorizer(jwt, service, "n/parent")
	headers, err := mint(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	c, err := jwt.Verify(strings.TrimPrefix(headers.Get("Authorization"), "Bearer "), "service")
	if err != nil || c.Expires != now.Add(time.Minute).Unix() {
		t.Fatal("short lifetime not enforced", c, err)
	}
	claims.Expires = now.Add(-time.Second).Unix()
	parent.Claims, _ = eventstore.Encode(claims)
	if _, err = mint(context.Background(), parent); err == nil {
		t.Fatal("minted expired parent")
	}
}
