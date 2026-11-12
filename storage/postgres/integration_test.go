//go:build integration

package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mejango/blossm-toolbox-go/eventstore"
)

var serial atomic.Uint64

func database(t *testing.T) (*Store, context.Context) {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Fatal("POSTGRES_URL is required for integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	name := fmt.Sprintf("blossm_test_%d_%d", time.Now().UnixNano(), serial.Add(1))
	s, err := Open(ctx, url, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := s.pool.Exec(cleanup, "DROP SCHEMA "+s.schema+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		s.Close(cleanup)
	})
	return s, ctx
}
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func event(root eventstore.Root, n int64) eventstore.Event {
	return eventstore.Event{Input: eventstore.Input{Root: root, Key: fmt.Sprintf("%s/%d", root.ID, n), Payload: json.RawMessage(`{"large":9007199254740993,"z":null,"a":"<>","fraction":1.00}`)}, Number: n, Hash: fmt.Sprintf("hash/%s/%d", root.ID, n)}
}
func appendOne(ctx context.Context, tx eventstore.Tx, roots ...eventstore.Root) error {
	if err := tx.LockRoots(ctx, roots); err != nil {
		return err
	}
	for _, root := range roots {
		n, err := tx.Count(ctx, root)
		if err != nil {
			return err
		}
		e := event(root, n)
		if err = tx.PutCount(ctx, root, n+1); err != nil {
			return err
		}
		if err = tx.PutEvent(ctx, e); err != nil {
			return err
		}
		if err = tx.PutPublication(ctx, eventstore.Publication{ID: e.Hash, Event: e}); err != nil {
			return err
		}
	}
	return nil
}
func TestNativeMissingAndRollback(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "root"}
	stop := errors.New("rollback")
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if err != nil || n != 0 {
			t.Fatalf("count %d %v", n, err)
		}
		_, err = r.Find(ctx, "missing")
		if !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.Snapshot(ctx, root)
		if !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.LastBlock(ctx)
		if !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.Projection(ctx, "missing")
		if !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	}))
	err := s.Update(ctx, func(tx eventstore.Tx) error {
		if err := appendOne(ctx, tx, root); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 0 {
			t.Fatal(n)
		}
		p, e := r.Pending(ctx, 10)
		if e != nil || len(p) != 0 {
			t.Fatal(p, e)
		}
		return err
	}))
}
func TestNativeConcurrentWriters(t *testing.T) {
	s, ctx := database(t)
	a, b := eventstore.Root{ID: "a"}, eventstore.Root{ID: "b"}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			roots := []eventstore.Root{a, b}
			if i%2 == 0 {
				roots[0], roots[1] = roots[1], roots[0]
			}
			errs <- s.Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, roots...) })
		}(i)
	}
	wg.Wait()
	close(errs)
	success := int64(0)
	for err := range errs {
		if err == nil {
			success++
		} else {
			t.Fatal(err)
		}
	}
	if success == 0 {
		t.Fatal("no progress")
	}
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		for _, root := range []eventstore.Root{a, b} {
			n, err := r.Count(ctx, root)
			if err != nil || n != success {
				t.Fatalf("count %d success %d error %v", n, success, err)
			}
			var seen int64
			err = r.Events(ctx, root, -1, func(e eventstore.Event) error {
				if e.Number != seen {
					t.Fatalf("gap: %d != %d", e.Number, seen)
				}
				seen++
				return nil
			})
			if err != nil {
				return err
			}
			if seen != success {
				t.Fatal(seen)
			}
		}
		return nil
	}))
}
func TestNativeRepeatableReads(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "repeatable"}
	check(t, s.Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, root) }))
	for _, update := range []bool{false, true} {
		fn := func(r eventstore.Reader) error {
			before, err := r.Count(ctx, root)
			if err != nil {
				return err
			}
			if err = s.Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, root) }); err != nil {
				return err
			}
			after, err := r.Count(ctx, root)
			if before != after {
				t.Fatalf("snapshot changed %d -> %d", before, after)
			}
			return err
		}
		if update {
			check(t, s.Update(ctx, func(tx eventstore.Tx) error { return fn(tx) }))
		} else {
			check(t, s.View(ctx, fn))
		}
	}
}
func TestNativeBytesSnapshotsBlocksProjectionPending(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "'unsafe';--"}
	e := event(root, 0)
	snap := eventstore.Snapshot{Root: root, Number: 0, State: json.RawMessage(`{"a":1}`), Events: []byte{0, 255, 1}, Hash: "snapshot"}
	block := eventstore.Block{Number: 0, Events: []byte{255, 0}, Snapshots: []byte{1, 2}, Transactions: []byte{3}, Signature: []byte{0, 255}, Hash: "block"}
	check(t, s.Update(ctx, func(tx eventstore.Tx) error {
		if err := appendOne(ctx, tx, root); err != nil {
			return err
		}
		if err := tx.PutSnapshot(ctx, snap); err != nil {
			return err
		}
		if err := tx.PutBlock(ctx, block); err != nil {
			return err
		}
		for i, version := range []int64{2, 1, 2, 3} {
			changed, err := tx.PutProjection(ctx, eventstore.Projection{ID: "projection", Root: root, Version: version, Body: json.RawMessage(`{"a":1}`), Deleted: version == 3})
			if err != nil {
				return err
			}
			if changed != (i == 0 || i == 3) {
				t.Fatalf("projection version %d changed=%v", version, changed)
			}
		}
		if err := tx.PutPublication(ctx, eventstore.Publication{ID: "second", Event: e}); err != nil {
			return err
		}
		return tx.FailPublication(ctx, e.Hash)
	}))
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		got, err := r.Find(ctx, e.Input.Key)
		if err != nil {
			return err
		}
		want, _ := json.Marshal(e)
		actual, _ := json.Marshal(got)
		if !bytes.Equal(want, actual) {
			t.Fatalf("canonical bytes changed %s %s", want, actual)
		}
		tx := r.(*transaction)
		var stored []byte
		if err = tx.tx.QueryRow(ctx, "SELECT data FROM "+s.schema+".events WHERE key=$1", e.Input.Key).Scan(&stored); err != nil {
			return err
		}
		if !bytes.Equal(want, stored) {
			t.Fatal("stored bytes differ")
		}
		gotSnap, err := r.Snapshot(ctx, root)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(snap, gotSnap) {
			t.Fatal(gotSnap)
		}
		gotBlock, err := r.LastBlock(ctx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(block, gotBlock) {
			t.Fatal(gotBlock)
		}
		p, err := r.Projection(ctx, "projection")
		if err != nil {
			return err
		}
		if p.Version != 3 || !p.Deleted {
			t.Fatal(p)
		}
		pending, err := r.Pending(ctx, 2)
		if err != nil {
			return err
		}
		if len(pending) != 2 || pending[0].ID != "second" || pending[1].Attempts != 1 {
			t.Fatal(pending)
		}
		_, err = r.Pending(ctx, 0)
		if err == nil {
			t.Fatal("accepted unlimited pending")
		}
		return nil
	}))
}
func TestNativeLargeBatchAndNestedCallbacks(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "large"}
	const size = 10000
	write := func(tx eventstore.Tx) error {
		if err := tx.LockRoots(ctx, []eventstore.Root{root}); err != nil {
			return err
		}
		for i := int64(0); i < size; i++ {
			if err := tx.PutEvent(ctx, event(root, i)); err != nil {
				return err
			}
		}
		return tx.PutCount(ctx, root, size)
	}
	stop := errors.New("late failure")
	err := s.Update(ctx, func(tx eventstore.Tx) error {
		if err := write(tx); err != nil {
			return err
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 0 {
			t.Fatal(n)
		}
		return err
	}))
	check(t, s.Update(ctx, write))
	check(t, s.Update(ctx, func(tx eventstore.Tx) error {
		seen := 0
		err := tx.Events(ctx, root, -1, func(e eventstore.Event) error {
			seen++
			if seen == 101 {
				_, err := tx.Find(ctx, e.Input.Key)
				if err != nil {
					return err
				}
				return tx.PutSnapshot(ctx, eventstore.Snapshot{Root: root, Number: 0, LastEvent: e.Number, Hash: "nested"})
			}
			return nil
		})
		if seen != size {
			t.Fatal(seen)
		}
		return err
	}))
}
func TestNativeCancellationAndCallbackFailure(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "cancel"}
	check(t, s.Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, root) }))
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.View(canceled, func(eventstore.Reader) error { t.Fatal("callback after cancel"); return nil }); err == nil {
		t.Fatal("missing cancellation")
	}
	stop := errors.New("callback failed")
	err := s.Update(ctx, func(tx eventstore.Tx) error {
		return tx.Events(ctx, root, -1, func(e eventstore.Event) error {
			if err := tx.PutCount(ctx, root, 99); err != nil {
				return err
			}
			return stop
		})
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 1 {
			t.Fatal(n)
		}
		return err
	}))
}

// The server commits, but its acknowledgement is lost to the caller.
type lostAcknowledgement struct{ pgx.Tx }

func (tx lostAcknowledgement) Commit(ctx context.Context) error {
	if err := tx.Tx.Commit(ctx); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
func TestNativeUncertainCommit(t *testing.T) {
	s, ctx := database(t)
	raw, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	check(t, err)
	defer rollback(raw)
	tx := &transaction{tx: raw, schema: s.schema}
	root := eventstore.Root{ID: "uncertain"}
	check(t, appendOne(ctx, tx, root))
	err = commit(ctx, lostAcknowledgement{raw})
	if !errors.Is(err, eventstore.ErrUncertain) || retryable(err) {
		t.Fatal(err)
	}
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 1 {
			t.Fatal(n)
		}
		return err
	}))
}
func TestNativeRetryBound(t *testing.T) {
	fixture, ctx := database(t)
	s, err := OpenWithRetry(ctx, os.Getenv("POSTGRES_URL"), strings.Trim(fixture.schema, `"`), RetryConfig{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
	check(t, err)
	defer s.Close(ctx)
	calls := 0
	err = s.Update(ctx, func(eventstore.Tx) error { calls++; return &pgconn.PgError{Code: "40001"} })
	if !retryable(err) || calls != 3 {
		t.Fatal(calls, err)
	}
	calls = 0
	err = s.Update(ctx, func(eventstore.Tx) error { calls++; return fmt.Errorf("%w: disconnected", eventstore.ErrUncertain) })
	if calls != 1 || !errors.Is(err, eventstore.ErrUncertain) {
		t.Fatal(calls, err)
	}
}
func TestNativeNamespaceIsolationAndInitialization(t *testing.T) {
	s, ctx := database(t)
	url := os.Getenv("POSTGRES_URL")
	namespace := strings.Trim(s.schema, `"`) + `";--`
	var wg sync.WaitGroup
	results := make(chan *Store, 2)
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, err := Open(ctx, url, namespace)
			if err != nil {
				errs <- err
			} else {
				results <- other
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var stores []*Store
	for other := range results {
		stores = append(stores, other)
		defer other.Close(ctx)
	}
	defer stores[0].pool.Exec(ctx, "DROP SCHEMA "+stores[0].schema+" CASCADE")
	root := eventstore.Root{ID: "isolated"}
	check(t, stores[0].Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, root) }))
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 0 {
			t.Fatal("namespace leaked")
		}
		return err
	}))
	check(t, stores[1].View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 1 {
			t.Fatal("initializer mismatch")
		}
		return err
	}))
}

func TestNativeUniqueConstraintsRollback(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "unique"}
	check(t, s.Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, root) }))
	for _, constraint := range []string{"key", "hash", "number", "publication"} {
		err := s.Update(ctx, func(tx eventstore.Tx) error {
			if err := tx.LockRoots(ctx, []eventstore.Root{root}); err != nil {
				return err
			}
			if err := tx.PutCount(ctx, root, 2); err != nil {
				return err
			}
			e := event(root, 1)
			original := event(root, 0)
			switch constraint {
			case "key":
				e.Input.Key = original.Input.Key
			case "hash":
				e.Hash = original.Hash
			case "number":
				e.Number = 0
			}
			if err := tx.PutEvent(ctx, e); err != nil {
				return err
			}
			return tx.PutPublication(ctx, eventstore.Publication{ID: original.Hash, Event: e})
		})
		if !errors.Is(err, eventstore.ErrConflict) {
			t.Fatalf("%s: %v", constraint, err)
		}
	}
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 1 {
			t.Fatal(n)
		}
		_, e := r.Find(ctx, event(root, 1).Input.Key)
		if !errors.Is(e, eventstore.ErrNotFound) {
			t.Fatal(e)
		}
		return err
	}))
}
func TestNativeLockCancellation(t *testing.T) {
	s, ctx := database(t)
	root := eventstore.Root{ID: "locked"}
	check(t, s.Update(ctx, func(tx eventstore.Tx) error { return appendOne(ctx, tx, root) }))
	raw, err := s.pool.Begin(ctx)
	check(t, err)
	defer rollback(raw)
	_, err = raw.Exec(ctx, "SELECT root FROM "+s.schema+".roots WHERE root=$1 FOR UPDATE", root.Key())
	check(t, err)
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = s.Update(deadline, func(tx eventstore.Tx) error { return tx.LockRoots(deadline, []eventstore.Root{root}) })
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked lock did not cancel: %v", err)
	}
}
func TestNativeNamespaceByteBoundaries(t *testing.T) {
	s, ctx := database(t)
	suffix := strings.Trim(s.schema, `"`)
	ascii := strings.Repeat("a", 63-len(suffix)) + suffix
	unicode := strings.Repeat("é", (63-len(suffix))/2) + strings.Repeat("a", (63-len(suffix))%2) + suffix
	for _, name := range []string{ascii, unicode} {
		other, err := Open(ctx, os.Getenv("POSTGRES_URL"), name)
		check(t, err)
		_, err = other.pool.Exec(ctx, "DROP SCHEMA "+other.schema+" CASCADE")
		check(t, err)
		other.Close(ctx)
	}
	for _, name := range []string{strings.Repeat("a", 64), strings.Repeat("é", 32)} {
		if other, err := Open(ctx, os.Getenv("POSTGRES_URL"), name); err == nil {
			other.Close(ctx)
			t.Fatal("accepted overlong namespace")
		}
	}
}

func TestNativeOrderedRootsAndLatestRecords(t *testing.T) {
	s, ctx := database(t)
	a, b := eventstore.Root{ID: "a"}, eventstore.Root{ID: "b"}
	check(t, s.Update(ctx, func(tx eventstore.Tx) error {
		if err := appendOne(ctx, tx, b, a); err != nil {
			return err
		}
		for _, n := range []int64{2, 1} {
			if err := tx.PutSnapshot(ctx, eventstore.Snapshot{Root: a, Number: n, Hash: fmt.Sprint("snap", n)}); err != nil {
				return err
			}
			if err := tx.PutBlock(ctx, eventstore.Block{Number: n, Hash: fmt.Sprint("block", n)}); err != nil {
				return err
			}
		}
		if _, err := tx.PutProjection(ctx, eventstore.Projection{ID: "bound", Root: a, Version: 1}); err != nil {
			return err
		}
		changed, err := tx.PutProjection(ctx, eventstore.Projection{ID: "bound", Root: b, Version: 2})
		if changed || !errors.Is(err, eventstore.ErrConflict) {
			t.Fatalf("projection rebound %v %v", changed, err)
		}
		if err := tx.DeletePublication(ctx, event(a, 0).Hash); err != nil {
			return err
		}
		if err := tx.FailPublication(ctx, "absent"); !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	}))
	check(t, s.View(ctx, func(r eventstore.Reader) error {
		var roots []eventstore.Root
		if err := r.Roots(ctx, func(root eventstore.Root) error {
			roots = append(roots, root)
			_, err := r.Count(ctx, root)
			return err
		}); err != nil {
			return err
		}
		if !reflect.DeepEqual(roots, []eventstore.Root{a, b}) {
			t.Fatal(roots)
		}
		snap, err := r.Snapshot(ctx, a)
		if err != nil {
			return err
		}
		if snap.Number != 2 {
			t.Fatal(snap)
		}
		block, err := r.LastBlock(ctx)
		if err != nil {
			return err
		}
		if block.Number != 2 {
			t.Fatal(block)
		}
		p, err := r.Pending(ctx, 1)
		if len(p) != 1 || p[0].ID != event(b, 0).Hash {
			t.Fatal(p)
		}
		return err
	}))
}

func TestNativeRetryCancellationAndDisabled(t *testing.T) {
	fixture, ctx := database(t)
	for _, attempts := range []int{1, 10} {
		s, err := OpenWithRetry(ctx, os.Getenv("POSTGRES_URL"), strings.Trim(fixture.schema, `"`), RetryConfig{MaxAttempts: attempts, BaseDelay: time.Second, MaxDelay: time.Second})
		check(t, err)
		canceled, cancel := context.WithCancel(ctx)
		calls := 0
		err = s.Update(canceled, func(eventstore.Tx) error { calls++; cancel(); return &pgconn.PgError{Code: "40001"} })
		cancel()
		s.Close(ctx)
		if calls != 1 {
			t.Fatalf("replayed canceled callback %d times", calls)
		}
		if attempts == 1 && !retryable(err) {
			t.Fatalf("disabled retries lost original error: %v", err)
		}
		if attempts > 1 && !errors.Is(err, context.Canceled) {
			t.Fatalf("backoff did not cancel: %v", err)
		}
	}
}
