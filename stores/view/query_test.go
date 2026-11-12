package view

import (
	"context"
	"fmt"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

// This fixture deliberately returns untrusted scope rows. View must enforce
// owner/source/deletion checks independently of the storage candidate provider.
type queryReader struct {
	eventstore.Reader
	rows   []eventstore.Projection
	calls  int
	broken bool
}

func (r *queryReader) Projections(_ context.Context, _, _, _, after string, limit int) ([]eventstore.Projection, error) {
	r.calls++
	if r.broken {
		return r.rows, nil
	}
	out := []eventstore.Projection{}
	for _, p := range r.rows {
		if p.ID > after {
			out = append(out, p)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

type queryBackend struct {
	eventstore.Backend
	reader eventstore.Reader
}

func (b queryBackend) View(_ context.Context, fn func(eventstore.Reader) error) error {
	return fn(b.reader)
}
func scope() Scope {
	return Scope{Network: "n", Service: "s", Domain: "d", Owner: []eventstore.Predicate{predicate("owner", `"alice"`)}}
}
func root(id string) eventstore.Root {
	return eventstore.Root{Network: "n", Service: "s", Domain: "d", ID: id}
}
func TestScopedListPagination(t *testing.T) {
	r := &queryReader{}
	for i := 0; i < 10002; i++ {
		r.rows = append(r.rows, eventstore.Projection{ID: fmt.Sprintf("%05d", i), Root: root("r"), Body: raw(`{"owner":"alice","tags":[{"id":1}],"nil":null}`)})
	}
	r.rows = append(r.rows, eventstore.Projection{ID: "other-owner", Root: root("r"), Body: raw(`{"owner":"bob"}`)}, eventstore.Projection{ID: "tombstone", Root: root("r"), Body: raw(`{"owner":"alice"}`), Deleted: true}, eventstore.Projection{ID: "wrong-scope", Root: eventstore.Root{ID: "r"}, Body: raw(`{"owner":"alice"}`)})
	s := Store{Backend: queryBackend{reader: r}, Scope: scope()}
	page, err := s.List(context.Background(), Query{Predicates: []eventstore.Predicate{predicate("tags.id", `1`), predicate("nil", `null`)}, Limit: 10000})
	if err != nil || len(page.Items) != 10000 || page.Total != 10002 || page.NextOffset == nil || *page.NextOffset != 10000 || r.calls < 2 {
		t.Fatalf("items %d total %d next %v calls %d err %v", len(page.Items), page.Total, page.NextOffset, r.calls, err)
	}
	page, err = s.List(context.Background(), Query{Offset: 10000, Limit: 10000})
	if err != nil || len(page.Items) != 2 || page.NextOffset != nil {
		t.Fatal(len(page.Items), err)
	}
	page, err = s.List(context.Background(), Query{})
	if err != nil || len(page.Items) != 50 {
		t.Fatal(len(page.Items), err)
	}
	page, err = s.List(context.Background(), Query{Predicates: []eventstore.Predicate{predicate("owner", `"bob"`)}})
	if err != nil || page.Total != 0 {
		t.Fatal(page.Total, err)
	}
}
func TestListFailsClosed(t *testing.T) {
	r := &queryReader{}
	s := Store{Backend: queryBackend{reader: r}, Scope: scope()}
	for _, q := range []Query{{Limit: 10001}, {Limit: -1}, {Offset: -1}, {Predicates: []eventstore.Predicate{predicate("x", `{}`)}}} {
		if _, err := s.List(context.Background(), q); err == nil {
			t.Fatal("invalid query accepted", q)
		}
	}
	if r.calls != 0 {
		t.Fatal("invalid query reached database")
	}
	s.Scope.Owner = nil
	if _, err := s.List(context.Background(), Query{}); err == nil {
		t.Fatal("unscoped query accepted")
	}
	s.Scope = scope()
	s.Backend = queryBackend{reader: nil}
	if _, err := s.List(context.Background(), Query{}); err == nil {
		t.Fatal("missing listing silently accepted")
	}
	r.rows = []eventstore.Projection{{ID: "b", Root: root("r"), Body: raw(`{"owner":"alice"}`)}, {ID: "a", Root: root("r"), Body: raw(`{"owner":"alice"}`)}}
	r.broken = true
	s.Backend = queryBackend{reader: r}
	if _, err := s.List(context.Background(), Query{}); err == nil {
		t.Fatal("unordered listing accepted")
	}
}

func TestInvalidMutationFailsBeforeBackend(t *testing.T) {
	s := Store{Scope: scope()}
	for _, m := range []Mutation{
		{ID: "a", Root: root("a"), Version: 9007199254740992, Body: raw(`{"owner":"alice"}`)},
		{ID: "a", Root: root("a"), Version: 0, Expected: func() *int64 { n := int64(9007199254740992); return &n }(), Body: raw(`{"owner":"alice"}`)},
		{ID: "a", Root: root("a"), Version: 0, Delete: true, Body: raw(`{}`)},
		{ID: "a", Root: root("a"), Version: 0, Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}}},
	} {
		if _, err := s.Batch(context.Background(), []Mutation{m}); err == nil {
			t.Fatal("invalid mutation accepted", m)
		}
	}
}

// Mongo may replay a transaction after a known transient failure. Results from
// the aborted read snapshot must never leak into the retry's successful page.
type replayingView struct {
	eventstore.Backend
	first, second eventstore.Reader
}

func (b replayingView) View(_ context.Context, fn func(eventstore.Reader) error) error {
	if err := fn(b.first); err != nil {
		return err
	}
	return fn(b.second)
}
func TestListResetsOnBackendRetry(t *testing.T) {
	first := &queryReader{rows: []eventstore.Projection{{ID: "aborted", Root: root("r"), Body: raw(`{"owner":"alice"}`)}}}
	second := &queryReader{rows: []eventstore.Projection{{ID: "committed", Root: root("r"), Body: raw(`{"owner":"alice"}`)}}}
	s := Store{Scope: scope(), Backend: replayingView{first: first, second: second}}
	page, err := s.List(context.Background(), Query{Limit: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "committed" || page.NextOffset != nil {
		t.Fatalf("aborted read snapshot leaked: %+v %v", page, err)
	}
}

func TestTombstoneValidationBeforeBackend(t *testing.T) {
	s := Store{Scope: scope()}
	for _, m := range []Mutation{
		{ID: "a", Root: root("a"), Version: 5, Tombstone: true},
		{ID: "a", Root: root("a"), Version: 5, Tombstone: true, Body: raw(`null`)},
		{ID: "a", Root: root("a"), Version: 5, Tombstone: true, Body: raw(`{"owner":"bob"}`)},
		{ID: "a", Root: root("a"), Version: 5, Tombstone: true, Body: raw(`{"owner":"alice"}`), Delete: true},
		{ID: "a", Root: root("a"), Version: 5, Tombstone: true, Body: raw(`{"owner":"alice"}`), Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}}},
	} {
		if _, err := s.Batch(context.Background(), []Mutation{m}); err == nil {
			t.Fatal("invalid tombstone accepted", m)
		}
	}
}
