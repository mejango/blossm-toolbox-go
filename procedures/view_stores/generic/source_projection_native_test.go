//go:build integration

package view_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/event_stores/generic"
	"github.com/mejango/blossm-toolbox-go/procedures/projection"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

type sourceReducer struct{}

func (sourceReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return canonicaljson.Canonical(p)
}
func (sourceReducer) Apply(_ json.RawMessage, e eventlib.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}

func TestNativeViewQueryWriteCannotPreemptProjection(t *testing.T) {
	for _, database := range []string{"postgres", "mongo"} {
		t.Run(database, func(t *testing.T) {
			views, ctx := view.NativeQueryStoreForTest(t, database)
			views.Name = "source-fenced"
			root := eventlib.Root{Network: "n", Service: "s", Domain: "d", ID: "row"}
			source := &eventstore.Store{Backend: views.Backend, Reducers: map[string]eventlib.Reducer{"s/d": sourceReducer{}}, Local: true}
			engine := projection.Engine{Aggregates: source, Views: views, Handlers: map[projection.Source]projection.Handler{{Network: "n", Service: "s", Domain: "d"}: func(_ context.Context, in projection.Input) (projection.Plan, error) {
				return projection.Plan{Mutations: []projection.Mutation{{ID: "row", Body: in.Aggregate.State}}}, nil
			}}}
			appendEvent := func(version int) {
				key := fmt.Sprintf("event-%d", version)
				_, err := source.Append(ctx, []eventlib.Input{{Root: root, Action: "set", Key: key, Payload: json.RawMessage(fmt.Sprintf(`{"owner":"alice","n":%d}`, version)), Created: "2026-11-12T00:00:00Z", Transaction: eventlib.Transaction{ID: key, Issued: "2026-11-12T00:00:00Z"}}})
				if err != nil {
					t.Fatal(err)
				}
			}
			play := func(version int64) {
				result, err := engine.Play(ctx, projection.Request{Root: root})
				if err != nil || len(result.Rows) != 1 || !result.Rows[0].Applied {
					t.Fatal("real projection suppressed", result, err)
				}
				p, err := views.Get(ctx, "row")
				want := fmt.Sprintf(`{"n":%d,"owner":"alice"}`, version)
				if err != nil || p.Version != version || string(p.Body) != want {
					t.Fatal(p, err)
				}
			}
			appendEvent(0)
			play(0)
			_, err := views.MutateWhere(ctx, view.Query{}, 1, func(_ context.Context, p eventlib.Projection) (view.Mutation, error) {
				return view.Mutation{ID: p.ID, Root: p.Root, Version: 1, Body: json.RawMessage(`{"owner":"alice","n":999}`)}, nil
			})
			if !errors.Is(err, view.ErrInvalid) {
				t.Fatal("invented source version accepted", err)
			}
			appendEvent(1)
			play(1)
			appendEvent(2)
			result, err := views.MutateWhere(ctx, view.Query{}, 1, func(_ context.Context, p eventlib.Projection) (view.Mutation, error) {
				return view.Mutation{ID: p.ID, Root: p.Root, Version: 2, Body: json.RawMessage(`{"owner":"alice","n":2}`)}, nil
			})
			if err != nil || len(result) != 1 || !result[0].Applied {
				t.Fatal("committed source mutation rejected", result, err)
			}
			appendEvent(3)
			play(3)
		})
	}
}
