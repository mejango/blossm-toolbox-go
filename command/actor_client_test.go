package command_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/transport"
)

type actorQueue struct {
	mu          sync.Mutex
	submissions []command.Submission
}

func (q *actorQueue) Enqueue(_ context.Context, in command.Submission) (command.Acceptance, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.submissions = append(q.submissions, in)
	return command.Acceptance{ID: "ack"}, nil
}

func TestActorNamespacePreservesNestedChildIDs(t *testing.T) {
	q := &actorQueue{}
	framework := transport.Framework{Authorize: func(r *http.Request) (transport.Identity, error) {
		var principal string
		switch r.Header.Get("Authorization") {
		case "Bearer alice":
			principal = "alice"
		case "Bearer bob":
			principal = "bob"
		default:
			return transport.Identity{}, errors.New("unauthorized")
		}
		raw, _ := json.Marshal(principal)
		return transport.Identity{Context: command.Context{"principal": raw}}, nil
	}, Commands: map[string]command.Procedure{"child": {Queue: q, Main: func(_ context.Context, in command.Request) (command.Result, error) {
		return command.Result{Events: []eventstore.Input{{Root: eventstore.Root{Network: "local", Service: "core", Domain: "example", ID: "one"}, Action: "set", Payload: in.Payload}}}, nil
	}}}}
	h, err := framework.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	wire, err := rpc.New(server.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	client := transport.CommandClient{RPC: wire, Target: "child-service", Authorize: func(_ context.Context, parent command.Request) (http.Header, error) {
		var actor string
		if err := json.Unmarshal(parent.Context["principal"], &actor); err != nil {
			return nil, err
		}
		return http.Header{"Authorization": []string{"Bearer " + actor}}, nil
	}}
	parent := command.Request{ID: "same-parent-id", Issued: "2026-11-12T00:00:00Z"}
	ids := []string{}
	for _, actor := range []string{`"alice"`, `"alice"`, `"bob"`} {
		parent.Context = command.Context{"principal": json.RawMessage(actor)}
		result, err := client.Execute(context.Background(), parent, "child", "reserve", command.Request{Async: true, Payload: json.RawMessage(`{}`)})
		if err != nil || len(result.Receipts) != 1 || result.Receipts[0].Outcome != eventstore.Queued {
			t.Fatal(result, err)
		}
		ids = append(ids, result.ID)
	}
	if ids[0] != ids[1] || ids[0] != ids[2] || ids[0] == parent.ID {
		t.Fatal("event namespace changed stable child ID contract", ids)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.submissions) != 3 {
		t.Fatal(q.submissions)
	}
	keys := []string{q.submissions[0].Events[0].Key, q.submissions[1].Events[0].Key, q.submissions[2].Events[0].Key}
	if keys[0] != keys[1] || keys[0] == keys[2] {
		t.Fatal("child event keys are not actor-scoped", keys)
	}
}
