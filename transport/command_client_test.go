package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blossm.local/backend/command"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/job"
	"blossm.local/backend/rpc"
)

func TestNestedCommandTwoServices(t *testing.T) {
	ctx := context.Background()
	trusted := Identity{Context: command.Context{"principal": json.RawMessage(`"owner"`)}, Claims: json.RawMessage(`{"sub":"owner"}`)}
	authorize := func(r *http.Request) (Identity, error) {
		if r.Header.Get("Authorization") != "Bearer delegated" {
			return Identity{}, errors.New("authentication required")
		}
		return trusted, nil
	}
	var mu sync.Mutex
	var children []command.Request
	child := Framework{Authorize: authorize, Commands: map[string]command.Procedure{"reserve": {Trace: &eventstore.Trace{Network: "local", Host: "child", Procedure: "command", Hash: "v1"}, Main: func(_ context.Context, in command.Request) (command.Result, error) {
		mu.Lock()
		children = append(children, in)
		mu.Unlock()
		if string(in.Claims) != `{"sub":"owner"}` || string(in.Context["principal"]) != `"owner"` {
			t.Error("delegated identity not injected")
		}
		return command.Result{Then: func(context.Context, []command.Receipt) (command.Contribution, error) {
			return command.Contribution{Body: map[string]json.RawMessage{"reserved": json.RawMessage(`true`)}}, nil
		}}, nil
	}}}}
	handler, err := child.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	childServer := httptest.NewServer(handler)
	defer childServer.Close()
	childRPC, err := rpc.New(childServer.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := CommandClient{RPC: childRPC, Target: "inventory", Authorize: func(_ context.Context, parent command.Request) (http.Header, error) {
		if string(parent.Context["principal"]) != `"owner"` {
			return nil, errors.New("untrusted parent")
		}
		return http.Header{"Authorization": []string{"Bearer delegated"}}, nil
	}}
	parent := Framework{Authorize: authorize, Commands: map[string]command.Procedure{"purchase": {Trace: &eventstore.Trace{Network: "local", Host: "parent", Procedure: "command", Hash: "v1"}, Main: func(ctx context.Context, in command.Request) (command.Result, error) {
		for _, operation := range []string{"item-a", "item-b"} {
			out, err := client.Execute(ctx, in, "reserve", operation, command.Request{ID: "ignored", Issued: "ignored", Payload: json.RawMessage(`{"count":1}`), Context: command.Context{"principal": json.RawMessage(`"forged"`)}, Claims: json.RawMessage(`{"sub":"forged"}`)})
			if err != nil {
				return command.Result{}, err
			}
			if string(out.Body["reserved"]) != "true" {
				return command.Result{}, errors.New("missing nested response")
			}
		}
		return command.Result{Response: command.Contribution{StatusCode: 200}}, nil
	}}}}
	parentHandler, err := parent.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	parentServer := httptest.NewServer(parentHandler)
	defer parentServer.Close()
	parentRPC, err := rpc.New(parentServer.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := command.Request{ID: "purchase-42", Issued: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{}`), Context: command.Context{"principal": json.RawMessage(`"forged"`)}, Claims: json.RawMessage(`{"sub":"forged"}`)}
	for i := 0; i < 2; i++ {
		if _, err = parentRPC.Do(ctx, http.MethodPost, "/commands/purchase", nil, in, http.Header{"Authorization": []string{"Bearer delegated"}}); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := append([]command.Request(nil), children...)
	mu.Unlock()
	if len(got) != 4 || got[0].ID == in.ID || got[0].ID == got[1].ID || got[0].ID != got[2].ID || got[1].ID != got[3].ID {
		t.Fatal("unstable or colliding child identity", got)
	}
	for _, request := range got {
		if request.Issued != in.Issued || request.Transaction.ID != in.ID || request.Transaction.Issued != in.Issued || len(request.Transaction.Path) != 2 || request.Transaction.Path[0].Host != "parent" || request.Transaction.Path[1].Host != "child" {
			t.Fatal("lost nested trace", request)
		}
	}
	other := client
	other.Target = "other-inventory"
	parentRequest := in
	parentRequest.Context = trusted.Context
	if _, err = other.Execute(ctx, parentRequest, "reserve", "item-a", command.Request{Payload: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	different := children[len(children)-1].ID
	mu.Unlock()
	if different == got[0].ID {
		t.Fatal("target identity omitted")
	}
}

func TestCommandClientFailureAndAuthorityBoundary(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var in command.Request
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		if in.Context != nil || len(in.Claims) != 0 || len(in.Transaction.Claims) != 0 {
			t.Error("body authority leaked")
		}
		if in.Payload != nil && string(in.Payload) == `{"wrong":true}` {
			in.ID = "wrong"
		}
		w.Header().Set("Blossm-Callback-Status", "failed")
		_ = json.NewEncoder(w).Encode(command.Response{ID: in.ID, TransactionID: in.Transaction.ID, Receipts: []command.Receipt{}})
	}))
	defer server.Close()
	wire, err := rpc.New(server.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := command.Request{ID: "parent", Issued: "2026-11-12T00:00:00Z", Claims: json.RawMessage(`{"sub":"trusted"}`), Transaction: eventstore.Transaction{Claims: json.RawMessage(`{"sub":"trusted"}`)}}
	c := CommandClient{RPC: wire, Target: "child", Authorize: func(context.Context, command.Request) (http.Header, error) {
		return http.Header{"Authorization": []string{"Bearer service"}}, nil
	}}
	out, err := c.Execute(context.Background(), p, "run", "one", command.Request{})
	var callback *command.CallbackError
	if !errors.As(err, &callback) || out.ID == "" || calls.Load() != 1 {
		t.Fatal(out, err)
	}
	if _, err = c.Execute(context.Background(), p, "run", "one", command.Request{Payload: json.RawMessage(`{"wrong":true}`)}); err == nil {
		t.Fatal("wrong response ID accepted")
	}
	before := calls.Load()
	if _, err = c.Execute(context.Background(), p, "../run", "one", command.Request{}); !errors.Is(err, command.ErrInvalid) {
		t.Fatal(err)
	}
	c.Authorize = func(context.Context, command.Request) (http.Header, error) {
		return nil, errors.New("auth unavailable")
	}
	if _, err = c.Execute(context.Background(), p, "run", "one", command.Request{}); err == nil || calls.Load() != before {
		t.Fatal("auth failure reached RPC", err)
	}
}

func TestCommandClientDeadlineNoRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	wire, err := rpc.New(server.URL, rpc.Options{Timeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	c := CommandClient{RPC: wire, Target: "child", Authorize: func(context.Context, command.Request) (http.Header, error) { return nil, nil }}
	_, err = c.Execute(context.Background(), command.Request{ID: "parent", Issued: "2026-11-12T00:00:00Z"}, "run", "one", command.Request{})
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 1 {
		t.Fatal("deadline or no-retry contract", calls.Load(), err)
	}
}

func TestFrameworkJobTrustedClaims(t *testing.T) {
	captured := make(chan job.Input, 1)
	f := Framework{Authorize: func(*http.Request) (Identity, error) {
		return Identity{Context: command.Context{"principal": json.RawMessage(`"trusted"`)}, Claims: json.RawMessage(`{"sub":"trusted"}`)}, nil
	}, Jobs: map[string]job.Procedure{"sweep": {Run: func(_ context.Context, in job.Input) error { captured <- in; return nil }}}}
	h, err := f.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	wire, err := rpc.New(server.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	in := job.Input{Payload: json.RawMessage(`{}`), Context: json.RawMessage(`{"principal":"forged"}`), Claims: json.RawMessage(`{"sub":"forged"}`)}
	if err = (job.Client{RPC: wire}).Execute(context.Background(), "/jobs/sweep", in, nil); err != nil {
		t.Fatal(err)
	}
	received := <-captured
	if !reflect.DeepEqual(received.Claims, json.RawMessage(`{"sub":"trusted"}`)) || string(received.Context) != `{"principal":"trusted"}` {
		t.Fatal("forged job authority", received)
	}
}
