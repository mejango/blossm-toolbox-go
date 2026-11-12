//go:build integration

package mongo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/event_stores/generic"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMain(m *testing.M) {
	uri := os.Getenv("MONGODB_URI")
	parsed, err := url.Parse(uri)
	if uri == "" || err != nil || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost" && parsed.Hostname() != "::1") {
		fmt.Fprintln(os.Stderr, "integration requires MONGODB_URI pointing to a disposable localhost replica set; tests never skip")
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func fixture(t *testing.T) (*Backend, context.Context) {
	t.Helper()
	return fixtureWithBudget(t, 60*time.Second)
}

func fixtureWithBudget(t *testing.T, budget time.Duration) (*Backend, context.Context) {
	t.Helper()
	return fixtureWithAttempts(t, budget, 30)
}
func fixtureWithAttempts(t *testing.T, budget time.Duration, attempts int) (*Backend, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	t.Cleanup(cancel)
	b, err := Open(ctx, Config{URI: os.Getenv("MONGODB_URI"), Database: fmt.Sprintf("blossm_mongo_test_%d", time.Now().UnixNano()), Namespace: "test", MaxAttempts: attempts})
	must(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		must(t, b.db.Drop(ctx))
		must(t, b.Close(ctx))
	})
	return b, ctx
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func testRoot(id string) eventlib.Root {
	return eventlib.Root{Network: "test", Service: "service", Domain: "domain", ID: id}
}
func testEvent(root eventlib.Root, number int64) eventlib.Event {
	e := eventlib.Event{Input: eventlib.Input{Root: root, Key: fmt.Sprintf("%s-%d", root.ID, number), Action: "set", Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"empty":{},"null":null,"number":9007199254740991,"string":"2"}`), Context: json.RawMessage(`{}`), Transaction: eventlib.Transaction{ID: "tx", Issued: "2026-11-12T00:00:00Z", Path: []eventlib.Trace{}, Claims: json.RawMessage(`{"aud":["one","two"]}`)}}, Number: number, Fingerprint: "fingerprint"}
	e.Fingerprint, _ = canonicaljson.Digest(e.Input)
	e.Hash, _ = canonicaljson.Digest(e)
	return e
}
func appendRaw(ctx context.Context, tx eventlib.Tx, e eventlib.Event) error {
	if err := tx.LockRoots(ctx, []eventlib.Root{e.Input.Root}); err != nil {
		return err
	}
	if err := tx.PutCount(ctx, e.Input.Root, e.Number+1); err != nil {
		return err
	}
	if err := tx.PutEvent(ctx, e); err != nil {
		return err
	}
	return tx.PutPublication(ctx, eventlib.Publication{ID: e.Hash, Event: e})
}

func TestNativeEmptyAndRoundtrip(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("root")
	e := testEvent(root, 0)
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		n, err := r.Count(ctx, root)
		must(t, err)
		if n != 0 {
			t.Fatal(n)
		}
		_, err = r.Find(ctx, "missing")
		if !errors.Is(err, eventlib.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.Snapshot(ctx, root)
		if !errors.Is(err, eventlib.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.LastBlock(ctx)
		if !errors.Is(err, eventlib.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.Projection(ctx, "missing")
		if !errors.Is(err, eventlib.ErrNotFound) {
			t.Fatal(err)
		}
		_, err = r.Pending(ctx, 0)
		if err == nil {
			t.Fatal("unbounded pending allowed")
		}
		return nil
	}))
	snap := eventlib.Snapshot{Root: root, Number: 0, LastEvent: 0, State: e.Input.Payload, Events: []byte{0, 255, 1}, Hash: "snapshot"}
	block := eventlib.Block{Number: 0, Hash: "block", Events: []byte{255, 0}, Snapshots: []byte{}, Transactions: []byte{1}, Signature: []byte{2}}
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		if err := appendRaw(ctx, tx, e); err != nil {
			return err
		}
		if err := tx.PutSnapshot(ctx, snap); err != nil {
			return err
		}
		return tx.PutBlock(ctx, block)
	}))
	var escaped eventlib.Reader
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		escaped = r
		got, err := r.Find(ctx, e.Input.Key)
		must(t, err)
		if !reflect.DeepEqual(got, e) {
			t.Fatalf("event changed: %#v", got)
		}
		must(t, eventstore.Verify(got))
		s, err := r.Snapshot(ctx, root)
		must(t, err)
		if !reflect.DeepEqual(s, snap) {
			t.Fatal("snapshot changed")
		}
		v, err := r.LastBlock(ctx)
		must(t, err)
		if !reflect.DeepEqual(v, block) {
			t.Fatal("block binary changed")
		}
		return nil
	}))
	if _, err := escaped.Count(ctx, root); err == nil {
		t.Fatal("escaped reader usable")
	}
	var raw bson.Raw
	must(t, b.collection("events").FindOne(ctx, id(e.Input.Key)).Decode(&raw))
	_, data, ok := raw.Lookup("body").BinaryOK()
	want, err := json.Marshal(e)
	must(t, err)
	if !ok || !bytes.Equal(data, want) {
		t.Fatal("event JSON not stored unchanged as BSON binary")
	}
}

func TestNativeRollbackAndConstraints(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("root")
	e := testEvent(root, 0)
	sentinel := errors.New("abort")
	err := b.Update(ctx, func(tx eventlib.Tx) error {
		if err := appendRaw(ctx, tx, e); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		n, err := r.Count(ctx, root)
		must(t, err)
		if n != 0 {
			t.Fatal(n)
		}
		p, err := r.Pending(ctx, 10)
		must(t, err)
		if len(p) != 0 {
			t.Fatal(p)
		}
		return nil
	}))
	must(t, b.Update(ctx, func(tx eventlib.Tx) error { return appendRaw(ctx, tx, e) }))
	for _, kind := range []string{"key", "hash", "root-number"} {
		t.Run(kind, func(t *testing.T) {
			other := testEvent(testRoot("other"), 1)
			switch kind {
			case "key":
				other.Input.Key = e.Input.Key
			case "hash":
				other.Hash = e.Hash
			case "root-number":
				other.Input.Root = e.Input.Root
				other.Number = e.Number
			}
			err := b.Update(ctx, func(tx eventlib.Tx) error { return appendRaw(ctx, tx, other) })
			if !errors.Is(err, eventlib.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
	snap := eventlib.Snapshot{Root: root, Number: 0, LastEvent: 0, Hash: "snap", State: json.RawMessage(`{}`)}
	block := eventlib.Block{Number: 0, Hash: "block"}
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		if err := tx.PutSnapshot(ctx, snap); err != nil {
			return err
		}
		return tx.PutBlock(ctx, block)
	}))
	for _, kind := range []string{"snapshot-hash", "snapshot-number", "block-hash", "block-number"} {
		t.Run(kind, func(t *testing.T) {
			err := b.Update(ctx, func(tx eventlib.Tx) error {
				s := snap
				v := block
				switch kind {
				case "snapshot-hash":
					s.Number = 1
					return tx.PutSnapshot(ctx, s)
				case "snapshot-number":
					s.Hash = "other"
					return tx.PutSnapshot(ctx, s)
				case "block-hash":
					v.Number = 1
				case "block-number":
					v.Hash = "other"
				}
				return tx.PutBlock(ctx, v)
			})
			if !errors.Is(err, eventlib.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeConcurrencyAndSnapshot(t *testing.T) {
	b, ctx := fixture(t)
	roots := []eventlib.Root{testRoot("a"), testRoot("b")}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- b.Update(ctx, func(tx eventlib.Tx) error {
				order := append([]eventlib.Root(nil), roots...)
				if i%2 == 1 {
					order[0], order[1] = order[1], order[0]
				}
				if err := tx.LockRoots(ctx, order); err != nil {
					return err
				}
				for _, root := range order {
					n, err := tx.Count(ctx, root)
					if err != nil {
						return err
					}
					if err = appendRaw(ctx, tx, testEvent(root, n)); err != nil {
						return err
					}
				}
				return nil
			})
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		for _, root := range roots {
			n, err := r.Count(ctx, root)
			must(t, err)
			if n != 12 {
				t.Fatal(n)
			}
			seen := int64(0)
			must(t, r.Events(ctx, root, -1, func(e eventlib.Event) error {
				if e.Number != seen {
					t.Fatal(e.Number, seen)
				}
				seen++
				return nil
			}))
			if seen != 12 {
				t.Fatal(seen)
			}
		}
		must(t, b.Update(ctx, func(tx eventlib.Tx) error { return appendRaw(ctx, tx, testEvent(roots[0], 12)) }))
		n, err := r.Count(ctx, roots[0])
		must(t, err)
		if n != 12 {
			t.Fatal("view lost snapshot", n)
		}
		return nil
	}))
}

func TestNativeCursorAndOutbox(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("root")
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		for i := int64(0); i < 260; i++ {
			if err := appendRaw(ctx, tx, testEvent(root, i)); err != nil {
				return err
			}
			if err := tx.PutCount(ctx, testRoot(fmt.Sprint(i)), 0); err != nil {
				return err
			}
		}
		return nil
	}))
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		count := 0
		must(t, r.Events(ctx, root, 0, func(e eventlib.Event) error {
			count++
			if e.Number != int64(count) {
				t.Fatal(e.Number)
			}
			return nil
		}))
		if count != 259 {
			t.Fatal(count)
		}
		count = 0
		must(t, r.Roots(ctx, func(eventlib.Root) error { count++; return nil }))
		if count != 261 {
			t.Fatal(count)
		}
		child, cancel := context.WithCancel(ctx)
		count = 0
		err := r.Events(child, root, -1, func(eventlib.Event) error { count++; cancel(); return nil })
		if !errors.Is(err, context.Canceled) || count != 1 {
			t.Fatal(err, count)
		}
		stop := errors.New("callback")
		count = 0
		err = r.Events(ctx, root, -1, func(eventlib.Event) error { count++; return stop })
		if !errors.Is(err, stop) || count != 1 {
			t.Fatal(err, count)
		}
		return nil
	}))
	var first string
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		pending, err := tx.Pending(ctx, 2)
		if err != nil {
			return err
		}
		if len(pending) != 2 {
			t.Fatal(len(pending))
		}
		first = pending[0].ID
		return tx.FailPublication(ctx, first)
	}))
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		p, err := tx.Pending(ctx, 260)
		if err != nil {
			return err
		}
		if p[259].ID != first || p[259].Attempts != 1 {
			t.Fatal("failed publication not rotated")
		}
		return tx.DeletePublication(ctx, first)
	}))
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		p, err := r.Pending(ctx, 300)
		if len(p) != 259 {
			t.Fatal(len(p))
		}
		return err
	}))
}

func TestNativeProjectionAndNamespace(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("root")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for version := int64(1); version <= 12; version++ {
		wg.Add(1)
		go func(v int64) {
			defer wg.Done()
			errs <- b.Update(ctx, func(tx eventlib.Tx) error {
				_, err := tx.PutProjection(ctx, eventlib.Projection{ID: "id", Root: root, Version: v, Body: json.RawMessage(fmt.Sprintf(`{"version":%d}`, v)), Deleted: v == 12})
				return err
			})
		}(version)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		p, err := tx.Projection(ctx, "id")
		if err != nil {
			return err
		}
		if p.Version != 12 || !p.Deleted {
			t.Fatal(p)
		}
		p.Version = 11
		p.Deleted = false
		ok, err := tx.PutProjection(ctx, p)
		if ok {
			t.Fatal("stale overwrite")
		}
		return err
	}))
	other, err := Open(ctx, Config{URI: os.Getenv("MONGODB_URI"), Database: b.db.Name(), Namespace: "other"})
	must(t, err)
	defer other.Close(ctx)
	must(t, other.View(ctx, func(r eventlib.Reader) error {
		_, err := r.Projection(ctx, "id")
		if !errors.Is(err, eventlib.ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	}))
	reopened, err := Open(ctx, Config{URI: os.Getenv("MONGODB_URI"), Database: b.db.Name(), Namespace: "test"})
	must(t, err)
	defer reopened.Close(ctx)
	must(t, reopened.View(ctx, func(r eventlib.Reader) error {
		p, err := r.Projection(ctx, "id")
		if p.Version != 12 {
			t.Fatal(p)
		}
		return err
	}))
}

// Requires enableTestCommands=1 on the disposable replica-set fixture.
func failCommands(t *testing.T, b *Backend, ctx context.Context, command, label string, times int) {
	t.Helper()
	data := bson.D{{Key: "failCommands", Value: bson.A{command}}, {Key: "errorLabels", Value: bson.A{label}}}
	if command == "commitTransaction" {
		// Execute the commit, then lose its acknowledgement. No dangling transaction
		// remains to hold locks during fixture cleanup.
		data = append(data, bson.E{Key: "writeConcernError", Value: bson.D{{Key: "code", Value: 64}, {Key: "errmsg", Value: "injected acknowledgement failure"}}})
	} else {
		data = append(data, bson.E{Key: "errorCode", Value: 112})
	}
	spec := bson.D{{Key: "configureFailPoint", Value: "failCommand"}, {Key: "mode", Value: bson.D{{Key: "times", Value: times}}}, {Key: "data", Value: data}}
	must(t, b.client.Database("admin").RunCommand(ctx, spec).Err())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		must(t, b.client.Database("admin").RunCommand(ctx, bson.D{{Key: "configureFailPoint", Value: "failCommand"}, {Key: "mode", Value: "off"}}).Err())
	})
}
func TestNativeRetryAndUncertain(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("root")
	failCommands(t, b, ctx, "update", "TransientTransactionError", 1)
	calls := 0
	must(t, b.Update(ctx, func(tx eventlib.Tx) error { calls++; return appendRaw(ctx, tx, testEvent(root, 0)) }))
	if calls != 2 {
		t.Fatal("transient did not retry", calls)
	}
	failCommands(t, b, ctx, "commitTransaction", "UnknownTransactionCommitResult", 2)
	calls = 0
	err := b.Update(ctx, func(tx eventlib.Tx) error { calls++; return appendRaw(ctx, tx, testEvent(root, 1)) })
	if !errors.Is(err, eventlib.ErrUncertain) || calls != 1 {
		t.Fatal("uncertain callback replayed", calls, err)
	}
	must(t, b.client.Database("admin").RunCommand(ctx, bson.D{{Key: "configureFailPoint", Value: "failCommand"}, {Key: "mode", Value: "off"}}).Err())
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		e, err := r.Find(ctx, testEvent(root, 1).Input.Key)
		if e.Number != 1 {
			t.Fatal("lost acknowledgement did not exercise committed data")
		}
		return err
	}))
}

func TestNativeRetryBudget(t *testing.T) {
	b, ctx := fixture(t)
	b.attempts = 3
	failCommands(t, b, ctx, "update", "TransientTransactionError", 3)
	calls := 0
	err := b.Update(ctx, func(tx eventlib.Tx) error { calls++; return tx.LockRoots(ctx, []eventlib.Root{testRoot("budget")}) })
	if err == nil || calls != 3 {
		t.Fatal("retry budget not enforced", calls, err)
	}
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		n, err := r.Count(ctx, testRoot("budget"))
		if n != 0 {
			t.Fatal("failed retry persisted state")
		}
		return err
	}))
}

func TestNativeLatestRecords(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("latest")
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		for _, number := range []int64{10, 2} {
			s := eventlib.Snapshot{Root: root, Number: number, LastEvent: number, State: json.RawMessage(`{"empty":{},"missing":null}`), Events: []byte{0, 255}, Hash: fmt.Sprint(number)}
			if err := tx.PutSnapshot(ctx, s); err != nil {
				return err
			}
			if err := tx.PutBlock(ctx, eventlib.Block{Number: number, Hash: fmt.Sprint(number)}); err != nil {
				return err
			}
		}
		return nil
	}))
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		s, err := r.Snapshot(ctx, root)
		if err != nil {
			return err
		}
		if s.Number != 10 || !bytes.Equal(s.Events, []byte{0, 255}) {
			t.Fatal(s)
		}
		block, err := r.LastBlock(ctx)
		if block.Number != 10 {
			t.Fatal(block)
		}
		return err
	}))
	var doc document
	must(t, b.collection("snapshots").FindOne(ctx, id("10")).Decode(&doc))
	var s eventlib.Snapshot
	must(t, json.Unmarshal(doc.Body, &s))
	want, err := json.Marshal(s)
	must(t, err)
	if !bytes.Equal(doc.Body, want) {
		t.Fatal("snapshot JSON bytes changed")
	}
}

func TestNativeViewDoesNotReplayStreams(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("single-attempt")
	must(t, b.Update(ctx, func(tx eventlib.Tx) error {
		if err := tx.LockRoots(ctx, []eventlib.Root{root}); err != nil {
			return err
		}
		for i := int64(0); i < 260; i++ {
			if err := tx.PutEvent(ctx, testEvent(root, i)); err != nil {
				return err
			}
		}
		return tx.PutCount(ctx, root, 260)
	}))
	t.Run("callback-error", func(t *testing.T) {
		calls, delivered := 0, 0
		stop := errors.New("consumer stopped")
		err := b.View(ctx, func(r eventlib.Reader) error {
			calls++
			return r.Events(ctx, root, -1, func(eventlib.Event) error { delivered++; return stop })
		})
		if !errors.Is(err, stop) || calls != 1 || delivered != 1 {
			t.Fatal("consumer callback replayed", calls, delivered, err)
		}
	})
	t.Run("transient-getMore-after-delivery", func(t *testing.T) {
		failCommands(t, b, ctx, "getMore", "TransientTransactionError", 1)
		calls, delivered := 0, 0
		err := b.View(ctx, func(r eventlib.Reader) error {
			calls++
			return r.Events(ctx, root, -1, func(e eventlib.Event) error {
				if e.Number != int64(delivered) {
					t.Fatalf("row repeated: number%d delivered%d", e.Number, delivered)
				}
				delivered++
				return nil
			})
		})
		if !hasLabel(err, "TransientTransactionError") || calls != 1 || delivered != 128 {
			t.Fatal("known abort replayed owned stream", calls, delivered, err)
		}
	})
}

func TestNativeDefaultRetryBudget(t *testing.T) {
	b, ctx := fixtureWithAttempts(t, 30*time.Second, 0)
	if b.attempts != 10 {
		t.Fatal("default total attempts", b.attempts)
	}
	failCommands(t, b, ctx, "update", "TransientTransactionError", 9)
	calls := 0
	root := testRoot("default-budget")
	must(t, b.Update(ctx, func(tx eventlib.Tx) error { calls++; return appendRaw(ctx, tx, testEvent(root, 0)) }))
	if calls != 10 {
		t.Fatal("default budget did not survive nine known aborts", calls)
	}
	must(t, b.View(ctx, func(r eventlib.Reader) error {
		n, err := r.Count(ctx, root)
		if n != 1 {
			t.Fatal("retry committed incorrect count", n)
		}
		return err
	}))
}
func TestNativeRetryCancellationAndFinalAttempt(t *testing.T) {
	b, ctx := fixture(t)
	for _, attempts := range []int{1, 3} {
		b.attempts = attempts
		child, cancel := context.WithCancel(ctx)
		calls := 0
		err := b.Update(child, func(eventlib.Tx) error { calls++; cancel(); return retryTransaction })
		cancel()
		if calls != 1 {
			t.Fatal("canceled callback repeated", calls)
		}
		if attempts == 1 && !errors.Is(err, retryTransaction) {
			t.Fatal("final attempt waited instead of preserving abort", err)
		}
		if attempts > 1 && !errors.Is(err, context.Canceled) {
			t.Fatal("retry wait ignored cancellation", err)
		}
	}
}
