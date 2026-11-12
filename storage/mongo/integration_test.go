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

	"github.com/mejango/blossm-toolbox-go/stores/event"
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	b, err := Open(ctx, Config{URI: os.Getenv("MONGODB_URI"), Database: fmt.Sprintf("blossm_mongo_test_%d", time.Now().UnixNano()), Namespace: "test", MaxAttempts: 30})
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
func testRoot(id string) eventstore.Root {
	return eventstore.Root{Network: "test", Service: "service", Domain: "domain", ID: id}
}
func testEvent(root eventstore.Root, number int64) eventstore.Event {
	e := eventstore.Event{Input: eventstore.Input{Root: root, Key: fmt.Sprintf("%s-%d", root.ID, number), Action: "set", Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"empty":{},"null":null,"number":9007199254740991,"string":"2"}`), Context: json.RawMessage(`{}`), Transaction: eventstore.Transaction{ID: "tx", Issued: "2026-11-12T00:00:00Z", Path: []eventstore.Trace{}, Claims: json.RawMessage(`{"aud":["one","two"]}`)}}, Number: number, Fingerprint: "fingerprint"}
	e.Fingerprint, _ = eventstore.Digest(e.Input)
	e.Hash, _ = eventstore.Digest(e)
	return e
}
func appendRaw(ctx context.Context, tx eventstore.Tx, e eventstore.Event) error {
	if err := tx.LockRoots(ctx, []eventstore.Root{e.Input.Root}); err != nil {
		return err
	}
	if err := tx.PutCount(ctx, e.Input.Root, e.Number+1); err != nil {
		return err
	}
	if err := tx.PutEvent(ctx, e); err != nil {
		return err
	}
	return tx.PutPublication(ctx, eventstore.Publication{ID: e.Hash, Event: e})
}

func TestNativeEmptyAndRoundtrip(t *testing.T) {
	b, ctx := fixture(t)
	root := testRoot("root")
	e := testEvent(root, 0)
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		n, err := r.Count(ctx, root)
		must(t, err)
		if n != 0 {
			t.Fatal(n)
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
		_, err = r.Pending(ctx, 0)
		if err == nil {
			t.Fatal("unbounded pending allowed")
		}
		return nil
	}))
	snap := eventstore.Snapshot{Root: root, Number: 0, LastEvent: 0, State: e.Input.Payload, Events: []byte{0, 255, 1}, Hash: "snapshot"}
	block := eventstore.Block{Number: 0, Hash: "block", Events: []byte{255, 0}, Snapshots: []byte{}, Transactions: []byte{1}, Signature: []byte{2}}
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if err := appendRaw(ctx, tx, e); err != nil {
			return err
		}
		if err := tx.PutSnapshot(ctx, snap); err != nil {
			return err
		}
		return tx.PutBlock(ctx, block)
	}))
	var escaped eventstore.Reader
	must(t, b.View(ctx, func(r eventstore.Reader) error {
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
	err := b.Update(ctx, func(tx eventstore.Tx) error {
		if err := appendRaw(ctx, tx, e); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	must(t, b.View(ctx, func(r eventstore.Reader) error {
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
	must(t, b.Update(ctx, func(tx eventstore.Tx) error { return appendRaw(ctx, tx, e) }))
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
			err := b.Update(ctx, func(tx eventstore.Tx) error { return appendRaw(ctx, tx, other) })
			if !errors.Is(err, eventstore.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
	snap := eventstore.Snapshot{Root: root, Number: 0, LastEvent: 0, Hash: "snap", State: json.RawMessage(`{}`)}
	block := eventstore.Block{Number: 0, Hash: "block"}
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.PutSnapshot(ctx, snap); err != nil {
			return err
		}
		return tx.PutBlock(ctx, block)
	}))
	for _, kind := range []string{"snapshot-hash", "snapshot-number", "block-hash", "block-number"} {
		t.Run(kind, func(t *testing.T) {
			err := b.Update(ctx, func(tx eventstore.Tx) error {
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
			if !errors.Is(err, eventstore.ErrConflict) {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeConcurrencyAndSnapshot(t *testing.T) {
	b, ctx := fixture(t)
	roots := []eventstore.Root{testRoot("a"), testRoot("b")}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- b.Update(ctx, func(tx eventstore.Tx) error {
				order := append([]eventstore.Root(nil), roots...)
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
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		for _, root := range roots {
			n, err := r.Count(ctx, root)
			must(t, err)
			if n != 12 {
				t.Fatal(n)
			}
			seen := int64(0)
			must(t, r.Events(ctx, root, -1, func(e eventstore.Event) error {
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
		must(t, b.Update(ctx, func(tx eventstore.Tx) error { return appendRaw(ctx, tx, testEvent(roots[0], 12)) }))
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
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
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
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		count := 0
		must(t, r.Events(ctx, root, 0, func(e eventstore.Event) error {
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
		must(t, r.Roots(ctx, func(eventstore.Root) error { count++; return nil }))
		if count != 261 {
			t.Fatal(count)
		}
		child, cancel := context.WithCancel(ctx)
		count = 0
		err := r.Events(child, root, -1, func(eventstore.Event) error { count++; cancel(); return nil })
		if !errors.Is(err, context.Canceled) || count != 1 {
			t.Fatal(err, count)
		}
		stop := errors.New("callback")
		count = 0
		err = r.Events(ctx, root, -1, func(eventstore.Event) error { count++; return stop })
		if !errors.Is(err, stop) || count != 1 {
			t.Fatal(err, count)
		}
		return nil
	}))
	var first string
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
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
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		p, err := tx.Pending(ctx, 260)
		if err != nil {
			return err
		}
		if p[259].ID != first || p[259].Attempts != 1 {
			t.Fatal("failed publication not rotated")
		}
		return tx.DeletePublication(ctx, first)
	}))
	must(t, b.View(ctx, func(r eventstore.Reader) error {
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
			errs <- b.Update(ctx, func(tx eventstore.Tx) error {
				_, err := tx.PutProjection(ctx, eventstore.Projection{ID: "id", Root: root, Version: v, Body: json.RawMessage(fmt.Sprintf(`{"version":%d}`, v)), Deleted: v == 12})
				return err
			})
		}(version)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
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
	must(t, other.View(ctx, func(r eventstore.Reader) error {
		_, err := r.Projection(ctx, "id")
		if !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	}))
	reopened, err := Open(ctx, Config{URI: os.Getenv("MONGODB_URI"), Database: b.db.Name(), Namespace: "test"})
	must(t, err)
	defer reopened.Close(ctx)
	must(t, reopened.View(ctx, func(r eventstore.Reader) error {
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
	must(t, b.Update(ctx, func(tx eventstore.Tx) error { calls++; return appendRaw(ctx, tx, testEvent(root, 0)) }))
	if calls != 2 {
		t.Fatal("transient did not retry", calls)
	}
	failCommands(t, b, ctx, "commitTransaction", "UnknownTransactionCommitResult", 2)
	calls = 0
	err := b.Update(ctx, func(tx eventstore.Tx) error { calls++; return appendRaw(ctx, tx, testEvent(root, 1)) })
	if !errors.Is(err, eventstore.ErrUncertain) || calls != 1 {
		t.Fatal("uncertain callback replayed", calls, err)
	}
	must(t, b.client.Database("admin").RunCommand(ctx, bson.D{{Key: "configureFailPoint", Value: "failCommand"}, {Key: "mode", Value: "off"}}).Err())
	must(t, b.View(ctx, func(r eventstore.Reader) error {
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
	err := b.Update(ctx, func(tx eventstore.Tx) error { calls++; return tx.LockRoots(ctx, []eventstore.Root{testRoot("budget")}) })
	if err == nil || calls != 3 {
		t.Fatal("retry budget not enforced", calls, err)
	}
	must(t, b.View(ctx, func(r eventstore.Reader) error {
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
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		for _, number := range []int64{10, 2} {
			s := eventstore.Snapshot{Root: root, Number: number, LastEvent: number, State: json.RawMessage(`{"empty":{},"missing":null}`), Events: []byte{0, 255}, Hash: fmt.Sprint(number)}
			if err := tx.PutSnapshot(ctx, s); err != nil {
				return err
			}
			if err := tx.PutBlock(ctx, eventstore.Block{Number: number, Hash: fmt.Sprint(number)}); err != nil {
				return err
			}
		}
		return nil
	}))
	must(t, b.View(ctx, func(r eventstore.Reader) error {
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
	var s eventstore.Snapshot
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
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.LockRoots(ctx, []eventstore.Root{root}); err != nil {
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
		err := b.View(ctx, func(r eventstore.Reader) error {
			calls++
			return r.Events(ctx, root, -1, func(eventstore.Event) error { delivered++; return stop })
		})
		if !errors.Is(err, stop) || calls != 1 || delivered != 1 {
			t.Fatal("consumer callback replayed", calls, delivered, err)
		}
	})
	t.Run("transient-getMore-after-delivery", func(t *testing.T) {
		failCommands(t, b, ctx, "getMore", "TransientTransactionError", 1)
		calls, delivered := 0, 0
		err := b.View(ctx, func(r eventstore.Reader) error {
			calls++
			return r.Events(ctx, root, -1, func(e eventstore.Event) error {
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
