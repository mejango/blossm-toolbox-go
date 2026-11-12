package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/lib/authentication_error"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/fact"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
	"github.com/mejango/blossm-toolbox-go/rpc/generic"
)

func TestFrameworkAuthorityAndStreamFailure(t *testing.T) {
	authorize := func(r *http.Request) (Identity, error) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			return Identity{}, errors.New("no")
		}
		return Identity{Claims: json.RawMessage(`{"sub":"trusted"}`)}, nil
	}
	f := Framework{Authorize: authorize, Facts: map[string]fact.Procedure{"broken": {StreamFn: func(ctx context.Context, in fact.Request, yield fact.Yield) error {
		if e := yield(ctx, json.RawMessage(`{"value":1}`)); e != nil {
			return e
		}
		return errors.New("source failed after output")
	}}}, Commands: map[string]command.Procedure{"echo": {Main: func(ctx context.Context, in command.Request) (command.Result, error) {
		if string(in.Claims) != `{"sub":"trusted"}` {
			t.Error("trusted identity not injected")
		}
		return command.Result{Response: command.Contribution{StatusCode: 200}}, nil
	}}}}
	h, e := f.Handler(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client, e := rpc.New(server.URL, rpc.Options{})
	if e != nil {
		t.Fatal(e)
	}
	headers := http.Header{"Authorization": []string{"Bearer fixture"}}
	request := command.Request{ID: "request", Issued: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{}`), Claims: json.RawMessage(`{"sub":"forged"}`)}
	if _, e = client.Do(context.Background(), "POST", "/commands/echo", nil, request, headers); e != nil {
		t.Fatal(e)
	}
	if _, e = client.Do(context.Background(), "POST", "/commands/echo", nil, request, nil); e == nil {
		t.Fatal("unauthenticated command")
	}
	count := 0
	fc := fact.Client{RPC: client}
	e = fc.Stream(context.Background(), "/facts/broken", map[string][]string{"stream": {"true"}}, headers, func(context.Context, json.RawMessage) error { count++; return nil })
	if e == nil || count != 1 {
		t.Fatalf("partial source looked successful count%d error%v", count, e)
	}
	if _, e = (Framework{}).Handler(context.Background()); e == nil {
		t.Fatal("missing auth accepted")
	}
}

func TestProcedureErrorStatus(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{view.ErrInvalid, 400}, {eventlib.ErrNotFound, 404}, {authn.ErrUnavailable, 503}, {fact.ErrInvalid, 400}, {command.ErrInvalid, 400}, {command.ErrForbidden, 403}, {command.ErrQueueUncertain, 503},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			response := httptest.NewRecorder()
			Wrap(func(http.ResponseWriter, *http.Request) error { return tc.err })(response, httptest.NewRequest("POST", "/", nil))
			if response.Code != tc.status {
				t.Fatalf("got %d want %d", response.Code, tc.status)
			}
		})
	}
}

func TestFrameworkMountsScopedAdapters(t *testing.T) {
	views := 0
	projections := 0
	f := Framework{
		Authorize: func(*http.Request) (Identity, error) { return Identity{}, nil },
		Views: &ViewGateway{Authorize: func(*http.Request, string, string) (view.Scope, error) {
			views++
			return view.Scope{}, authn.ErrForbidden
		}},
		Projections: &ProjectionRoutes{Resolve: func(*http.Request, ProjectionOperation) (ProjectionBinding, error) {
			projections++
			return ProjectionBinding{}, authn.ErrForbidden
		}},
	}
	handler, err := f.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/view-stores/items", ""}, {"POST", "/projections/items/play", `{"rootId":"one"}`},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
		if response.Code != 403 {
			t.Fatalf("%s %d", tc.path, response.Code)
		}
	}
	if views != 1 || projections != 1 {
		t.Fatal("scoped authorizers not invoked")
	}
}
