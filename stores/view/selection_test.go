package view

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

type selectionBackend struct {
	eventstore.Backend
	rows   map[string]eventstore.Projection
	writes int
}
type selectionTx struct {
	eventstore.Tx
	rows   map[string]eventstore.Projection
	writes *int
}

func (b *selectionBackend) View(_ context.Context, fn func(eventstore.Reader) error) error {
	return fn(&selectionTx{rows: b.rows, writes: &b.writes})
}
func (b *selectionBackend) Update(_ context.Context, fn func(eventstore.Tx) error) error {
	rows := map[string]eventstore.Projection{}
	for k, v := range b.rows {
		v.Body = append([]byte(nil), v.Body...)
		rows[k] = v
	}
	if err := fn(&selectionTx{rows: rows, writes: &b.writes}); err != nil {
		return err
	}
	b.rows = rows
	return nil
}

// These fixture rows have two committed source events. Native fixtures append both.
func (tx *selectionTx) Count(context.Context, eventstore.Root) (int64, error) { return 2, nil }
func (tx *selectionTx) LockRoots(context.Context, []eventstore.Root) error    { return nil }
func (tx *selectionTx) Projection(_ context.Context, id string) (eventstore.Projection, error) {
	p, ok := tx.rows[id]
	if !ok {
		return p, eventstore.ErrNotFound
	}
	return p, nil
}
func (tx *selectionTx) PutProjection(_ context.Context, p eventstore.Projection) (bool, error) {
	*tx.writes++
	tx.rows[p.ID] = p
	return true, nil
}
func (tx *selectionTx) Projections(_ context.Context, _, _, _, after string, limit int) ([]eventstore.Projection, error) {
	ids := []string{}
	for id, p := range tx.rows {
		if id > after && !p.Deleted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	rows := []eventstore.Projection{}
	for _, id := range ids[:min(limit, len(ids))] {
		rows = append(rows, tx.rows[id])
	}
	return rows, nil
}
func selectionBuilder(_ context.Context, p eventstore.Projection) (Mutation, error) {
	return Mutation{ID: p.ID, Root: p.Root, Version: 1, Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}}}, nil
}
func TestMutateWhereAtomicSelectionAndBudget(t *testing.T) {
	b := &selectionBackend{rows: map[string]eventstore.Projection{}}
	s := &Store{Backend: b, Scope: scope(), Name: "selected"}
	exerciseSelection(t, s, context.Background())
	before := b.writes
	_, err := s.MutateWhere(context.Background(), Query{}, 1, selectionBuilder)
	if !errors.Is(err, ErrInvalid) || b.writes != before {
		t.Fatal("over-budget wrote", err, b.writes, before)
	}
}

// Shared with native tests, proving the same expected rollback/state on a real
// backend rather than assuming the fixture's transaction copy is sufficient.
func exerciseSelection(t *testing.T, s *Store, ctx context.Context) {
	t.Helper()
	for _, row := range []struct{ id, body string }{{"a", `{"owner":"alice","groups":["g1"],"n":1}`}, {"b", `{"owner":"alice","groups":["g2"],"n":"bad"}`}, {"c", `{"owner":"alice","groups":["other"],"n":10}`}} {
		_, err := s.Batch(ctx, []Mutation{{ID: row.id, Root: root(row.id), Version: 0, Body: raw(row.body)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	other := *s
	other.Scope.Owner = []eventstore.Predicate{predicate("owner", `"bob"`)}
	if _, err := other.Batch(ctx, []Mutation{{ID: "foreign", Root: root("foreign"), Version: 0, Body: raw(`{"owner":"bob","groups":["g1"],"n":1}`)}}); err != nil {
		t.Fatal(err)
	}
	s.Scope.OwnerAny = [][]eventstore.Predicate{{predicate("groups", `"g1"`)}, {predicate("groups", `"g2"`)}}
	called := 0
	_, err := s.MutateWhere(ctx, Query{}, 1, func(ctx context.Context, p eventstore.Projection) (Mutation, error) {
		called++
		return selectionBuilder(ctx, p)
	})
	if !errors.Is(err, ErrInvalid) || called != 0 {
		t.Fatal("selection truncated or callback ran", called, err)
	}
	_, err = s.MutateWhere(ctx, Query{}, 2, selectionBuilder)
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("late operation failure expected", err)
	}
	p, err := s.Get(ctx, "a")
	if err != nil || p.Version != 0 || string(p.Body) != `{"groups":["g1"],"n":1,"owner":"alice"}` {
		t.Fatal("late failure did not roll back", p, err)
	}
	result, err := s.MutateWhere(ctx, Query{Ranges: []Range{{Path: "n", Kind: NumberValue, Lower: bound(`0`, false)}}}, 2, selectionBuilder)
	if err != nil || len(result) != 1 || result[0].ID != "a" || !result[0].Applied {
		t.Fatal(result, err)
	}
	p, err = s.Get(ctx, "a")
	if err != nil || p.Version != 1 {
		t.Fatal(p, err)
	}
	result, err = s.MutateWhere(ctx, Query{Predicates: []eventstore.Predicate{predicate("owner", `"bob"`)}}, 2, selectionBuilder)
	if err != nil || len(result) != 0 {
		t.Fatal("query widened authority", result, err)
	}
	p, err = other.Get(ctx, "foreign")
	if err != nil || p.Version != 0 {
		t.Fatal("foreign row modified", p, err)
	}
	_, err = s.MutateWhere(ctx, Query{}, 2, func(_ context.Context, p eventstore.Projection) (Mutation, error) {
		m, _ := selectionBuilder(ctx, p)
		m.ID = "foreign"
		return m, nil
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("selection identity override", err)
	}
	failure := errors.New("builder unavailable")
	_, err = s.MutateWhere(ctx, Query{}, 2, func(context.Context, eventstore.Projection) (Mutation, error) { return Mutation{}, failure })
	if !errors.Is(err, failure) || errors.Is(err, ErrInvalid) {
		t.Fatal("builder error relabeled", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = s.MutateWhere(canceled, Query{}, 2, selectionBuilder)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestMutateWhereRejectsPaginationAndMissingBudget(t *testing.T) {
	s := Store{Scope: scope()}
	for _, tc := range []struct {
		q       Query
		maximum int
	}{{Query{}, 0}, {Query{}, 10001}, {Query{Offset: 1}, 2}, {Query{Limit: 1}, 2}} {
		if _, err := s.MutateWhere(context.Background(), tc.q, tc.maximum, selectionBuilder); !errors.Is(err, ErrInvalid) {
			t.Fatal(tc, err)
		}
	}
}
