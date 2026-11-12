package view

import (
	"context"
	"errors"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

func TestRequiredOwnerAnyEmptyDeniesEveryOperation(t *testing.T) {
	b := &selectionBackend{rows: map[string]eventstore.Projection{"a": {ID: "a", Root: root("a"), Version: 0, Body: raw(`{"owner":"alice","n":1}`)}}}
	s := Store{Backend: b, Scope: scope()}
	s.Scope.RequireOwnerAny = true
	s.Scope.OwnerAny = [][]eventstore.Predicate{}
	s.Scope = copyScope(s.Scope)
	page, err := s.List(context.Background(), Query{})
	if err != nil || page.Total != 0 {
		t.Fatalf("zero groups listed tenant: %+v %v", page, err)
	}
	if _, err := s.Get(context.Background(), "a"); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("zero groups read tenant", err)
	}
	called := false
	result, err := s.MutateWhere(context.Background(), Query{}, 1, func(ctx context.Context, p eventstore.Projection) (Mutation, error) {
		called = true
		return selectionBuilder(ctx, p)
	})
	if err != nil || len(result) != 0 || called || b.writes != 0 {
		t.Fatal("zero groups selected mutation", result, called, err)
	}
}
func TestMutateWhereRejectsInventedSourceFrontier(t *testing.T) {
	b := &selectionBackend{rows: map[string]eventstore.Projection{"a": {ID: "a", Root: root("a"), Version: 0, Body: raw(`{"owner":"alice","n":1}`)}}}
	s := Store{Backend: b, Scope: scope(), SourceCount: func(context.Context, eventstore.Root) (int64, error) { return 1, nil }}
	result, err := s.MutateWhere(context.Background(), Query{}, 1, selectionBuilder)
	if !errors.Is(err, ErrInvalid) || result != nil || b.writes != 0 || b.rows["a"].Version != 0 {
		t.Fatal("invented future source fence", result, err, b.rows["a"])
	}
}

func TestQuerySourceProofPreservesFailuresAndRejectsOldFrontier(t *testing.T) {
	for _, cause := range []error{errors.New("count unavailable"), eventstore.ErrUncertain, context.Canceled} {
		b := &selectionBackend{rows: map[string]eventstore.Projection{"a": {ID: "a", Root: root("a"), Version: 0, Body: raw(`{"owner":"alice","n":1}`)}}}
		s := Store{Backend: b, Scope: scope(), SourceCount: func(_ context.Context, r eventstore.Root) (int64, error) {
			if r != root("a") {
				t.Fatal(r)
			}
			return 0, cause
		}}
		if _, err := s.MutateWhere(context.Background(), Query{}, 1, selectionBuilder); !errors.Is(err, cause) || errors.Is(err, ErrInvalid) || b.writes != 0 {
			t.Fatal(err, b.writes)
		}
	}
	for _, count := range []int64{0, 1, 3} {
		b := &selectionBackend{rows: map[string]eventstore.Projection{"a": {ID: "a", Root: root("a"), Version: 0, Body: raw(`{"owner":"alice","n":1}`)}}}
		s := Store{Backend: b, Scope: scope(), SourceCount: func(context.Context, eventstore.Root) (int64, error) { return count, nil }}
		if _, err := s.MutateWhere(context.Background(), Query{}, 1, selectionBuilder); !errors.Is(err, ErrInvalid) || b.writes != 0 {
			t.Fatal("non-current source version", count, err)
		}
	}
}
func TestRequiredOwnerAnySurvivesCompositeScopeCopies(t *testing.T) {
	b := &selectionBackend{rows: map[string]eventstore.Projection{"a": {ID: "a", Root: root("a"), Version: 0, Body: raw(`{"owner":"alice"}`)}}}
	auth := scope()
	auth.RequireOwnerAny = true
	_, err := ExecuteComposite(context.Background(), auth, Query{}, map[string]ComponentReader{"rows": BackendComponent{Backend: b}}, func(_ context.Context, q Query, visible Scope, c *Components) (Page, error) {
		visible.RequireOwnerAny = false
		page, err := c.List("rows", q)
		if err != nil || page.Total != 0 {
			t.Fatal(page, err)
		}
		if _, err := c.Get("rows", "a"); !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		return page, err
	})
	if err != nil {
		t.Fatal(err)
	}
}
