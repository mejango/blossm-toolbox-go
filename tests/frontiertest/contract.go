//go:build integration

// Package frontiertest exercises identical bounded-progress contracts natively.
package frontiertest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/event_stores/generic"
)

type replacement struct{}

func (replacement) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return canonicaljson.Canonical(p)
}
func (replacement) Apply(_ json.RawMessage, e eventlib.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func root(id string) eventlib.Root {
	return eventlib.Root{Network: "fair", Service: "service", Domain: "domain", ID: id}
}
func pending(t *testing.T, b eventlib.Backend, ctx context.Context, want ...string) {
	t.Helper()
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		roots, err := r.(eventlib.FrontierReader).PendingRoots(ctx, 10)
		if err != nil {
			return err
		}
		got := make([]string, 0, len(roots))
		for _, root := range roots {
			got = append(got, root.ID)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("frontier got %v want %v", got, want)
		}
		return nil
	}))
}

type aborting struct {
	eventlib.Backend
	err error
}

func (b aborting) Update(ctx context.Context, fn func(eventlib.Tx) error) error {
	return b.Backend.Update(ctx, func(tx eventlib.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		return b.err
	})
}

// Fair keeps a dirty after every block, requiring bounded progress for b/c both
// before and after they have snapshots. Reopening must retain service priority.
func Fair(t *testing.T, b eventlib.Backend, ctx context.Context, reopen func() eventlib.Backend) {
	t.Helper()
	store := eventstore.Store{Backend: b, Local: true, Reducers: map[string]eventlib.Reducer{"service/domain": replacement{}}}
	sequence := 0
	appendRoots := func(ids ...string) {
		t.Helper()
		inputs := []eventlib.Input{}
		for _, id := range ids {
			sequence++
			key := fmt.Sprintf("%s-%d", id, sequence)
			inputs = append(inputs, eventlib.Input{Root: root(id), Key: key, Action: "set", Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"ready":true}`), Transaction: eventlib.Transaction{ID: key, Issued: "2026-11-12T00:00:00Z"}})
		}
		_, err := store.Append(ctx, inputs)
		must(t, err)
	}
	build := func(want string) {
		t.Helper()
		block, err := store.BuildBlock(ctx, 1, nil)
		must(t, err)
		var snapshots []eventlib.Snapshot
		must(t, json.Unmarshal(block.Snapshots, &snapshots))
		if len(snapshots) != 1 || snapshots[0].Root.ID != want {
			t.Fatalf("maxRoots1 selected %v want %s", snapshots, want)
		}
	}
	appendRoots("a", "b", "c")
	pending(t, b, ctx, "a", "b", "c")
	build("a")
	appendRoots("a")
	pending(t, b, ctx, "b", "c", "a")
	build("b")
	appendRoots("a")
	build("c")
	appendRoots("b", "c")
	// All roots now have snapshots; a's most recent snapshot is still oldest.
	b = reopen()
	store.Backend = b
	pending(t, b, ctx, "a", "b", "c")
	build("a")
	appendRoots("a")
	pending(t, b, ctx, "b", "c", "a")
	build("b")
	appendRoots("a")
	build("c")
	appendRoots("b", "c")
	pending(t, b, ctx, "a", "b", "c")
	// An aborted block has already written snapshots and priority metadata in its
	// transaction. Neither can move the first root to the back after rollback.
	stop := errors.New("abort after block and snapshot credit")
	failed := store
	failed.Backend = aborting{Backend: b, err: stop}
	_, err := failed.BuildBlock(ctx, 1, nil)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	pending(t, b, ctx, "a", "b", "c")
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		block, err := r.LastBlock(ctx)
		if err == nil && block.Number != 5 {
			t.Fatalf("aborted block persisted: %d", block.Number)
		}
		return err
	}))
	// Standalone snapshot progress does not receive completed-block service credit.
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		snapshot, err := tx.Snapshot(ctx, root("a"))
		if err != nil {
			return err
		}
		snapshot.Number++
		snapshot.Hash = ""
		snapshot.Hash, err = canonicaljson.Digest(snapshot)
		if err != nil {
			return err
		}
		return tx.PutSnapshot(ctx, snapshot)
	}))
	pending(t, b, ctx, "a", "b", "c")
	build("a")
	appendRoots("a")
	pending(t, b, ctx, "b", "c", "a")
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		_, err := r.(eventlib.FrontierReader).PendingRoots(ctx, 0)
		if err == nil {
			t.Fatal("zero limit accepted")
		}
		return nil
	}))
}

// CreditOnlyCurrent proves multiple block writes cannot reuse earlier snapshot
// credit and an out-of-order historical snapshot cannot move a current head.
func CreditOnlyCurrent(t *testing.T, b eventlib.Backend, ctx context.Context) {
	t.Helper()
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		if err := tx.LockRoots(ctx, []eventlib.Root{root("a"), root("z")}); err != nil {
			return err
		}
		for _, id := range []string{"a", "z"} {
			if err := tx.PutCount(ctx, root(id), 2); err != nil {
				return err
			}
		}
		for i, entry := range []struct {
			id     string
			number int64
		}{{"z", 2}, {"a", 0}, {"z", 1}} {
			snapshot := eventlib.Snapshot{Root: root(entry.id), Number: entry.number, LastEvent: 0, State: json.RawMessage(`{}`), Hash: fmt.Sprintf("snapshot-%d", i)}
			if err := tx.PutSnapshot(ctx, snapshot); err != nil {
				return err
			}
			if err := tx.PutBlock(ctx, eventlib.Block{Number: int64(i), Hash: fmt.Sprintf("block-%d", i)}); err != nil {
				return err
			}
		}
		return nil
	}))
	pending(t, b, ctx, "z", "a")
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		snapshot, err := r.Snapshot(ctx, root("z"))
		if err == nil && snapshot.Number != 2 {
			t.Fatal("historical snapshot replaced head")
		}
		return err
	}))
}
