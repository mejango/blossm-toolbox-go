//go:build integration

package view

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/storage/postgres"
)

func nativeStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Fatal("POSTGRES_URL is required for view integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	ns := fmt.Sprintf("view_test_%d", time.Now().UnixNano())
	backend, err := postgres.Open(ctx, url, ns)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		backend.Close(cleanup)
		conn, err := pgx.Connect(cleanup, url)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close(cleanup)
		if _, err = conn.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{ns}.Sanitize()+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	return &Store{Backend: backend, Scope: scope()}, ctx
}
func version(n int64) *int64 { return &n }
func replacement(id string, n int64, body string) Mutation {
	return Mutation{ID: id, Root: root(id), Version: n, Body: raw(body)}
}
func mustBatch(t *testing.T, s *Store, ctx context.Context, m ...Mutation) []Result {
	t.Helper()
	result, err := s.Batch(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestNativeViewScopedLifecycle(t *testing.T) {
	s, ctx := nativeStore(t)
	mustBatch(t, s, ctx, replacement("a", 0, `{"owner":"alice","n":0,"roles":[{"id":"member"}]}`))
	bob := *s
	bob.Scope.Owner = []eventstore.Predicate{predicate("owner", `"bob"`)}
	mustBatch(t, &bob, ctx, replacement("b", 0, `{"owner":"bob","n":0}`))
	if _, err := s.Get(ctx, "b"); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("cross-owner read", err)
	}
	if _, err := s.Batch(ctx, []Mutation{replacement("b", 1, `{"owner":"alice"}`)}); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("cross-owner takeover", err)
	}
	patched := mustBatch(t, s, ctx, Mutation{ID: "a", Root: root("a"), Version: 1, Expected: version(0), Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`2`)}}})
	if !patched[0].Applied || patched[0].Version != 1 {
		t.Fatal(patched)
	}
	duplicate := mustBatch(t, s, ctx, Mutation{ID: "a", Root: root("a"), Version: 1, Expected: version(0), Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`2`)}}})
	if duplicate[0].Applied {
		t.Fatal("duplicate patch applied twice")
	}
	page, err := s.List(ctx, Query{Predicates: []eventstore.Predicate{predicate("roles.id", `"member"`), predicate("n", `2`)}, Limit: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "a" {
		t.Fatal(page, err)
	}
	results := mustBatch(t, s, ctx, Mutation{ID: "a", Root: root("a"), Version: 2, Delete: true})
	if !results[0].Applied {
		t.Fatal(results)
	}
	if _, err := s.Get(ctx, "a"); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("tombstone exposed", err)
	}
	results = mustBatch(t, s, ctx, replacement("a", 1, `{"owner":"alice","n":99}`))
	if results[0].Applied {
		t.Fatal("stale state resurrected tombstone")
	}
	page, err = s.List(ctx, Query{})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	mustBatch(t, s, ctx, replacement("a", 3, `{"owner":"alice","n":3}`))
	if p, err := s.Get(ctx, "a"); err != nil || p.Version != 3 {
		t.Fatal(p, err)
	}
}
func TestNativeViewAtomicFailureAndConditions(t *testing.T) {
	s, ctx := nativeStore(t)
	mustBatch(t, s, ctx, replacement("a", 0, `{"owner":"alice","n":1}`), replacement("b", 0, `{"owner":"alice","n":"text"}`))
	first := Mutation{ID: "a", Root: root("a"), Version: 1, Expected: version(0), Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`4`)}}}
	bad := Mutation{ID: "b", Root: root("b"), Version: 1, Expected: version(0), Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}}}
	if result, err := s.Batch(ctx, []Mutation{first, bad}); err == nil || result != nil {
		t.Fatal(result, err)
	}
	p, err := s.Get(ctx, "a")
	if err != nil || p.Version != 0 || string(p.Body) != `{"n":1,"owner":"alice"}` {
		t.Fatal("partial batch committed", p, err)
	}
	first.Expected = version(4)
	if _, err := s.Batch(ctx, []Mutation{first}); !errors.Is(err, eventstore.ErrConflict) {
		t.Fatal("missing base version accepted", err)
	}
	first.Expected = nil
	if _, err := s.Batch(ctx, []Mutation{first}); err == nil {
		t.Fatal("unguarded patch accepted")
	}
	first.Expected = version(0)
	first.If = []eventstore.Predicate{predicate("n", `9`)}
	results := mustBatch(t, s, ctx, first)
	if results[0].Applied {
		t.Fatal("false condition applied")
	}
	transfer := replacement("a", 1, `{"owner":"bob"}`)
	if _, err := s.Batch(ctx, []Mutation{transfer}); err == nil {
		t.Fatal("owner transfer allowed")
	}
	rebound := replacement("a", 1, `{"owner":"alice"}`)
	rebound.Root.ID = "another-source"
	if _, err := s.Batch(ctx, []Mutation{rebound}); !errors.Is(err, eventstore.ErrConflict) {
		t.Fatal("source rebound", err)
	}
	conditionalCreate := replacement("new", 0, `{"owner":"alice"}`)
	conditionalCreate.If = []eventstore.Predicate{predicate("owner", `"alice"`)}
	results = mustBatch(t, s, ctx, conditionalCreate)
	if results[0].Applied {
		t.Fatal("conditional write upserted")
	}
	if _, err := s.Get(ctx, "new"); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal(err)
	}
}
func TestNativeViewConcurrentVersionsAndPatch(t *testing.T) {
	s, ctx := nativeStore(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := int64(0); i < 8; i++ {
		wg.Add(1)
		go func(n int64) {
			defer wg.Done()
			_, err := s.Batch(ctx, []Mutation{replacement("a", n, fmt.Sprintf(`{"owner":"alice","n":%d}`, n))})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.Get(ctx, "a")
	if err != nil || p.Version != 7 || string(p.Body) != `{"n":7,"owner":"alice"}` {
		t.Fatal("stale winner", p, err)
	}
	var applied atomic.Int32
	errs = make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Batch(ctx, []Mutation{{ID: "a", Root: root("a"), Version: 8, Expected: version(7), Operations: []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}}}})
			if err == nil && r[0].Applied {
				applied.Add(1)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err = s.Get(ctx, "a")
	if err != nil || applied.Load() != 1 || string(p.Body) != `{"n":8,"owner":"alice"}` {
		t.Fatal("patch repeated", p, applied.Load(), err)
	}
}
func TestNativeViewListingPagesAndCancellation(t *testing.T) {
	s, ctx := nativeStore(t)
	mutations := make([]Mutation, 520)
	for i := range mutations {
		mutations[i] = replacement(fmt.Sprintf("id-%04d", i), 0, fmt.Sprintf(`{"owner":"alice","n":%d}`, i))
	}
	mustBatch(t, s, ctx, mutations...)
	page, err := s.List(ctx, Query{Limit: 100, Offset: 250})
	if err != nil || page.Total != 520 || len(page.Items) != 100 || page.Items[0].ID != "id-0250" || page.NextOffset == nil || *page.NextOffset != 350 {
		t.Fatal(page.Total, len(page.Items), err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.List(canceled, Query{}); !errors.Is(err, context.Canceled) {
		t.Fatal("query ignored cancellation", err)
	}
	if _, err := s.Batch(canceled, []Mutation{replacement("canceled", 0, `{"owner":"alice"}`)}); !errors.Is(err, context.Canceled) {
		t.Fatal("mutation ignored cancellation", err)
	}
}

// Inject a committed write between real storage pages while preserving the
// underlying Reader, so List must retain one consistent total and result set.
type hookedBackend struct {
	eventstore.Backend
	hook func() error
}
type hookedReader struct {
	eventstore.Reader
	list ProjectionReader
	hook func() error
}

func (b hookedBackend) View(ctx context.Context, fn func(eventstore.Reader) error) error {
	return b.Backend.View(ctx, func(r eventstore.Reader) error {
		return fn(&hookedReader{Reader: r, list: r.(ProjectionReader), hook: b.hook})
	})
}
func (r *hookedReader) Projections(ctx context.Context, n, s, d, after string, limit int) ([]eventstore.Projection, error) {
	rows, err := r.list.Projections(ctx, n, s, d, after, limit)
	if err != nil {
		return nil, err
	}
	if r.hook != nil {
		hook := r.hook
		r.hook = nil
		if err = hook(); err != nil {
			return nil, err
		}
	}
	return rows, nil
}
func TestNativeViewRepeatablePagination(t *testing.T) {
	s, ctx := nativeStore(t)
	changes := make([]Mutation, 257)
	for i := range changes {
		changes[i] = replacement(fmt.Sprintf("id-%04d", i), 0, `{"owner":"alice"}`)
	}
	mustBatch(t, s, ctx, changes...)
	reader := *s
	reader.Backend = hookedBackend{Backend: s.Backend, hook: func() error {
		_, err := s.Batch(ctx, []Mutation{replacement("zz-new", 0, `{"owner":"alice"}`)})
		return err
	}}
	page, err := reader.List(ctx, Query{Limit: 10000})
	if err != nil || page.Total != 257 || len(page.Items) != 257 {
		t.Fatalf("list snapshot changed: total %d items %d err %v", page.Total, len(page.Items), err)
	}
	page, err = s.List(ctx, Query{Limit: 10000})
	if err != nil || page.Total != 258 {
		t.Fatal(page.Total, err)
	}
}

func TestNativeViewTombstoneBeforeCreate(t *testing.T) {
	s, ctx := nativeStore(t)
	tombstone := Mutation{ID: "future", Root: root("future"), Version: 5, Tombstone: true, Body: raw(`{"owner":"alice","reason":"removed"}`)}
	result := mustBatch(t, s, ctx, tombstone)
	if !result[0].Applied || result[0].Version != 5 {
		t.Fatal(result)
	}
	err := s.Backend.View(ctx, func(r eventstore.Reader) error {
		p, err := r.Projection(ctx, "future")
		if err != nil {
			return err
		}
		if !p.Deleted || p.Version != 5 || string(p.Body) != `{"owner":"alice","reason":"removed"}` {
			t.Fatal("missing durable tombstone", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result = mustBatch(t, s, ctx, replacement("future", 4, `{"owner":"alice","n":4}`))
	if result[0].Applied || result[0].Version != 5 {
		t.Fatal("older create resurrected tombstone", result)
	}
	if _, err = s.Get(ctx, "future"); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal(err)
	}
	page, err := s.List(ctx, Query{})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	result = mustBatch(t, s, ctx, tombstone)
	if result[0].Applied {
		t.Fatal("duplicate tombstone applied")
	}
	rebound := tombstone
	rebound.Root = root("other")
	rebound.Version = 6
	if _, err = s.Batch(ctx, []Mutation{rebound}); !errors.Is(err, eventstore.ErrConflict) {
		t.Fatal("tombstone source rebound", err)
	}
	bob := *s
	bob.Scope.Owner = []eventstore.Predicate{predicate("owner", `"bob"`)}
	forged := tombstone
	forged.Body = raw(`{"owner":"bob"}`)
	forged.Version = 6
	if _, err = bob.Batch(ctx, []Mutation{forged}); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("tombstone owner takeover", err)
	}
	result = mustBatch(t, s, ctx, replacement("future", 6, `{"owner":"alice","n":6}`))
	if !result[0].Applied {
		t.Fatal("newer state rejected", result)
	}
	result = mustBatch(t, s, ctx, tombstone)
	if result[0].Applied || result[0].Version != 6 {
		t.Fatal("stale tombstone deleted newer state", result)
	}
	p, err := s.Get(ctx, "future")
	if err != nil || p.Version != 6 {
		t.Fatal(p, err)
	}
	// Ordinary remove intentionally remains a no-op when no view exists.
	result = mustBatch(t, s, ctx, Mutation{ID: "ordinary", Root: root("ordinary"), Version: 100, Delete: true})
	if result[0].Applied {
		t.Fatal("ordinary delete created a tombstone", result)
	}
	result = mustBatch(t, s, ctx, replacement("ordinary", 1, `{"owner":"alice"}`))
	if !result[0].Applied {
		t.Fatal("ordinary remove semantics changed", result)
	}
}
