//go:build integration

package view

import (
	mongoBackend "blossm.local/backend/storage/mongo"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/url"
	"os"
	"testing"
	"time"

	"blossm.local/backend/eventstore"
)

func TestNativeViewMutateWhereAtomicSelection(t *testing.T) {
	for _, name := range []string{"postgres", "mongo"} {
		t.Run(name, func(t *testing.T) {
			s, ctx := nativeQueryStore(t, name)
			s.Name = "selected"
			for _, id := range []string{"a", "b", "c"} {
				seedSelectionEvents(t, s, ctx, root(id))
			}
			exerciseSelection(t, s, ctx)
		})
	}
}
func TestNativeViewNamedIsolationSameRootIDVersion(t *testing.T) {
	for _, name := range []string{"postgres", "mongo"} {
		t.Run(name, func(t *testing.T) { s, ctx := nativeQueryStore(t, name); exerciseNamedIsolation(t, s, ctx) })
	}
}
func exerciseNamedIsolation(t *testing.T, s *Store, ctx context.Context) {
	s.Name = "first"
	other := *s
	other.Name = "second"
	mustBatch(t, s, ctx, replacement("same", 1, `{"owner":"alice","n":1}`))
	mustBatch(t, &other, ctx, replacement("same", 1, `{"owner":"alice","n":2}`))
	for _, store := range []*Store{s, &other} {
		p, err := store.Get(ctx, "same")
		if err != nil || p.ID != "same" || p.Version != 1 {
			t.Fatal(p, err)
		}
		page, err := store.List(ctx, Query{})
		if err != nil || page.Total != 1 {
			t.Fatal(page, err)
		}
	}
	mustBatch(t, s, ctx, Mutation{ID: "same", Root: root("same"), Version: 2, Tombstone: true, Body: raw(`{"owner":"alice"}`)})
	if _, err := s.Get(ctx, "same"); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal(err)
	}
	if p, err := other.Get(ctx, "same"); err != nil || p.Version != 1 {
		t.Fatal(p, err)
	}
}

func nativeQueryStore(t *testing.T, name string) (*Store, context.Context) {
	t.Helper()
	if name == "postgres" {
		return nativeStore(t)
	}
	uri := os.Getenv("MONGODB_URI")
	parsed, err := url.Parse(uri)
	if err != nil || uri == "" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		t.Fatal("MONGODB_URI must point to a disposable localhost replica set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	database := fmt.Sprintf("view_query_test_%d", time.Now().UnixNano())
	b, err := mongoBackend.Open(ctx, mongoBackend.Config{URI: uri, Database: database, Namespace: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		defer func() {
			if err := b.Close(cleanup); err != nil {
				t.Error(err)
			}
		}()
		client, err := driver.Connect(options.Client().ApplyURI(uri))
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Disconnect(cleanup)
		if err := client.Database(database).Drop(cleanup); err != nil {
			t.Error(err)
		}
	})
	return &Store{Backend: b, Scope: scope()}, ctx
}

// Exported only in the integration test build for the external Engine regression.
func NativeQueryStoreForTest(t *testing.T, name string) (*Store, context.Context) {
	return nativeQueryStore(t, name)
}

type selectionSourceReducer struct{}

func (selectionSourceReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return eventstore.Canonical(p)
}
func (selectionSourceReducer) Apply(_ json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}
func seedSelectionEvents(t *testing.T, s *Store, ctx context.Context, r eventstore.Root) {
	source := eventstore.Store{Backend: s.Backend, Reducers: map[string]eventstore.Reducer{"s/d": selectionSourceReducer{}}, Local: true}
	for i := 0; i < 2; i++ {
		key := fmt.Sprintf("selection-%s-%d", r.ID, i)
		_, err := source.Append(ctx, []eventstore.Input{{Root: r, Action: "set", Key: key, Created: "2026-11-12T00:00:00Z", Payload: raw(`{"owner":"alice"}`), Transaction: eventstore.Transaction{ID: key, Issued: "2026-11-12T00:00:00Z"}}})
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestNativeViewRequiredOwnerAnyEmpty(t *testing.T) {
	for _, name := range []string{"postgres", "mongo"} {
		t.Run(name, func(t *testing.T) {
			s, ctx := nativeQueryStore(t, name)
			mustBatch(t, s, ctx, replacement("a", 0, `{"owner":"alice"}`))
			s.Scope.RequireOwnerAny = true
			s.Scope.OwnerAny = [][]eventstore.Predicate{}
			s.Scope = copyScope(s.Scope)
			page, err := s.List(ctx, Query{})
			if err != nil || page.Total != 0 {
				t.Fatal(page, err)
			}
			if _, err := s.Get(ctx, "a"); !errors.Is(err, eventstore.ErrNotFound) {
				t.Fatal(err)
			}
			result, err := s.MutateWhere(ctx, Query{}, 1, func(context.Context, eventstore.Projection) (Mutation, error) {
				t.Fatal("empty group selected row")
				return Mutation{}, nil
			})
			if err != nil || len(result) != 0 {
				t.Fatal(result, err)
			}
		})
	}
}
