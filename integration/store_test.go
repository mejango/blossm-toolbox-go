//go:build integration

package integration

import (
	"blossm.local/backend/audit"
	"blossm.local/backend/command"
	"blossm.local/backend/rpc"
	"blossm.local/backend/transport"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blossm.local/backend/eventstore"
	mongoStore "blossm.local/backend/storage/mongo"
	"blossm.local/backend/storage/postgres"
)

type counter struct{}

func (counter) Normalize(action string, p json.RawMessage) (json.RawMessage, error) {
	if action != "add" {
		return nil, errors.New("unknown action")
	}
	var v struct {
		Amount int `json:"amount"`
	}
	if e := eventstore.Decode(p, &v); e != nil {
		return nil, e
	}
	if v.Amount < 0 {
		return nil, errors.New("negative amount")
	}
	return eventstore.Encode(v)
}
func (counter) Apply(state json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	var s struct {
		Total int `json:"total"`
	}
	var p struct {
		Amount int `json:"amount"`
	}
	if err := json.Unmarshal(state, &s); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(e.Input.Payload, &p); err != nil {
		return nil, err
	}
	s.Total += p.Amount
	return eventstore.Encode(s)
}
func newStore(t *testing.T, kind string) *eventstore.Store {
	t.Helper()
	ctx := context.Background()
	ns := fmt.Sprintf("test_%d", time.Now().UnixNano())
	var b eventstore.Backend
	var e error
	if kind == "postgres" {
		uri := os.Getenv("POSTGRES_URL")
		if uri == "" {
			t.Fatal("POSTGRES_URL required")
		}
		b, e = postgres.Open(ctx, uri, ns)
	} else {
		uri := os.Getenv("MONGODB_URI")
		if uri == "" {
			t.Fatal("MONGODB_URI required")
		}
		b, e = mongoStore.Open(ctx, mongoStore.Config{URI: uri, Database: "blossm_test", Namespace: ns, MaxAttempts: 20})
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := b.Close(context.Background()); e != nil {
			t.Error(e)
		}
	})
	return &eventstore.Store{Backend: b, Reducers: map[string]eventstore.Reducer{"test/counter": counter{}}}
}
func input(root, key string) eventstore.Input {
	return eventstore.Input{Root: eventstore.Root{Network: "local", Service: "test", Domain: "counter", ID: root}, Action: "add", Key: key, Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"amount":1}`), Transaction: eventstore.Transaction{ID: key, Issued: "2026-11-12T00:00:00Z"}}
}

type publishFunc func(context.Context, eventstore.Publication) error

func (f publishFunc) Publish(c context.Context, p eventstore.Publication) error { return f(c, p) }
func TestNativeContract(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			t.Run("atomic-duplicate-and-replay", func(t *testing.T) {
				s := newStore(t, kind)
				ctx := context.Background()
				a := input("a", "a")
				b := input("b", "b")
				wrong := int64(9)
				b.Expected = &wrong
				if _, e := s.Append(ctx, []eventstore.Input{a, b}); !errors.Is(e, eventstore.ErrConflict) {
					t.Fatalf("want conflict: %v", e)
				}
				state, e := s.Aggregate(ctx, a.Root)
				if e != nil || state.Version != -1 {
					t.Fatalf("rollback: %+v %v", state, e)
				}
				b.Expected = nil
				r, e := s.Append(ctx, []eventstore.Input{a, b, a})
				if e != nil {
					t.Fatal(e)
				}
				if r[2].Outcome != eventstore.Duplicate {
					t.Fatal(r)
				}
				for _, v := range r {
					if e := eventstore.Verify(v.Event); e != nil {
						t.Fatal(e)
					}
				}
				a.Payload = json.RawMessage(`{"amount":2}`)
				if _, e = s.Append(ctx, []eventstore.Input{a}); !errors.Is(e, eventstore.ErrIdempotency) {
					t.Fatal(e)
				}
				state, e = s.Aggregate(ctx, b.Root)
				if e != nil || string(state.State) != `{"total":1}` {
					t.Fatalf("replay %s %v", state.State, e)
				}
			})
			t.Run("concurrent-counts", func(t *testing.T) {
				s := newStore(t, kind)
				ctx := context.Background()
				var wg sync.WaitGroup
				var committed atomic.Int64
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						_, e := s.Append(ctx, []eventstore.Input{input("same", fmt.Sprint(i))})
						if e != nil {
							t.Error(e)
							return
						}
						committed.Add(1)
					}(i)
				}
				wg.Wait()
				a, e := s.Aggregate(ctx, input("same", "").Root)
				if e != nil || a.Version != committed.Load()-1 {
					t.Fatalf("counts %+v %v", a, e)
				}
			})
			t.Run("outbox-restart-and-ack-window", func(t *testing.T) {
				s := newStore(t, kind)
				ctx := context.Background()
				if _, e := s.Append(ctx, []eventstore.Input{input("a", "a")}); e != nil {
					t.Fatal(e)
				}
				var id string
				err := s.Deliver(ctx, publishFunc(func(_ context.Context, p eventstore.Publication) error {
					id = p.ID
					return errors.New("publish failed")
				}), 1)
				if err == nil {
					t.Fatal("hidden failure")
				}
				restarted := &eventstore.Store{Backend: s.Backend, Reducers: s.Reducers}
				if e := restarted.Deliver(ctx, publishFunc(func(_ context.Context, p eventstore.Publication) error {
					if p.ID != id {
						t.Error("unstable identity")
					}
					return nil
				}), 1); e != nil {
					t.Fatal(e)
				}
				if e := s.Deliver(ctx, publishFunc(func(context.Context, eventstore.Publication) error {
					t.Error("acknowledged notice repeated")
					return nil
				}), 1); e != nil {
					t.Fatal(e)
				}
			})
			t.Run("snapshots-blocks-query-frontier", func(t *testing.T) {
				s := newStore(t, kind)
				ctx := context.Background()
				batch := make([]eventstore.Input, 125)
				for i := range batch {
					batch[i] = input(fmt.Sprint(i), fmt.Sprint(i))
				}
				if _, e := s.Append(ctx, batch); e != nil {
					t.Fatal(e)
				}
				b, e := s.BuildBlock(ctx, 200, nil)
				if e != nil {
					t.Fatal(e)
				}
				var snapshots []eventstore.Snapshot
				if e = json.Unmarshal(b.Snapshots, &snapshots); e != nil || len(snapshots) != 125 {
					t.Fatalf("snapshots %d %v", len(snapshots), e)
				}
				matches, e := s.Query(ctx, "local", "test", "counter", []eventstore.Predicate{{Path: "total", Value: json.RawMessage(`1`)}})
				if e != nil || len(matches) != 125 {
					t.Fatalf("query %d %v", len(matches), e)
				}
				b, e = s.BuildBlock(ctx, 200, nil)
				if e != nil {
					t.Fatal(e)
				}
				if string(b.Events) != "null" {
					t.Fatalf("repeated frontier %s", b.Events)
				}
				if _, e = s.Append(ctx, []eventstore.Input{input("new", "late")}); e != nil {
					t.Fatal(e)
				}
				b, e = s.BuildBlock(ctx, 200, nil)
				if e != nil {
					t.Fatal(e)
				}
				var events []eventstore.Event
				if e = json.Unmarshal(b.Events, &events); e != nil || len(events) != 1 {
					t.Fatalf("late frontier %d %v", len(events), e)
				}
			})
			t.Run("local-bypass-and-stale-projection", func(t *testing.T) {
				s := newStore(t, kind)
				s.Local = true
				ctx := context.Background()
				in := input("a", "a")
				if _, e := s.Append(ctx, []eventstore.Input{in}); e != nil {
					t.Fatal(e)
				}
				s.Local = false
				if e := s.Deliver(ctx, publishFunc(func(context.Context, eventstore.Publication) error { t.Error("local notice"); return nil }), 10); e != nil {
					t.Fatal(e)
				}
				p := eventstore.Projection{ID: "view", Root: in.Root, Version: 2, Body: json.RawMessage(`{"total":3}`)}
				ok, e := s.Project(ctx, p)
				if e != nil || !ok {
					t.Fatal(ok, e)
				}
				p.Version = 1
				p.Body = json.RawMessage(`{"total":2}`)
				ok, e = s.Project(ctx, p)
				if e != nil || ok {
					t.Fatal(ok, e)
				}
			})
		})
	}
}

func TestHistoricalReplay(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t, kind)
			ctx := context.Background()
			a := input("history", "first")
			a.Context = json.RawMessage(`{"owner":"a"}`)
			b := input("history", "second")
			b.Created = "2026-11-13T00:00:00Z"
			b.Context = json.RawMessage(`{"scene":"b"}`)
			if _, e := s.Append(ctx, []eventstore.Input{a, b}); e != nil {
				t.Fatal(e)
			}
			if _, e := s.BuildBlock(ctx, 10, nil); e != nil {
				t.Fatal(e)
			}
			cutoff, _ := time.Parse(time.RFC3339, "2026-11-12T00:00:00Z")
			h, e := s.History(ctx, a.Root, eventstore.HistoryOptions{ThroughCreated: &cutoff, IncludeEvents: true})
			if e != nil || h.LastIncludedNumber != 0 || len(h.Events) != 1 || string(h.State) != `{"total":1}` || string(h.Context) != `{"owner":"a"}` {
				t.Fatalf("history %+v %v", h, e)
			}
		})
	}
}

// A reducer may use mutable working buffers, but never owns committed metadata.
type mutatingReducer struct{ counter }

func (m mutatingReducer) Apply(state json.RawMessage, event eventstore.Event) (json.RawMessage, error) {
	next, err := m.counter.Apply(state, event)
	for _, raw := range []json.RawMessage{event.Input.Payload, event.Input.Context, event.Input.Transaction.Claims} {
		for i := range raw {
			raw[i] = ' '
		}
	}
	if event.Input.Expected != nil {
		*event.Input.Expected = 99
	}
	if len(event.Input.Transaction.Path) > 0 {
		event.Input.Transaction.Path[0].Host = "mutated"
	}
	return next, err
}
func TestReducerCannotMutateCommittedBytes(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t, kind)
			s.Reducers["test/counter"] = mutatingReducer{}
			ctx := context.Background()
			in := input("immutable", "immutable")
			expected := int64(0)
			in.Expected = &expected
			in.Context = json.RawMessage(`{"owner":"original"}`)
			in.Transaction.Claims = json.RawMessage(`{"sub":"original"}`)
			in.Transaction.Path = []eventstore.Trace{{Network: "local", Host: "original", Procedure: "test", Hash: "hash"}}
			receipts, err := s.Append(ctx, []eventstore.Input{in})
			if err != nil {
				t.Fatal(err)
			}
			if err = eventstore.Verify(receipts[0].Event); err != nil {
				t.Fatal(err)
			}
			if expected != 0 || in.Transaction.Path[0].Host != "original" {
				t.Fatal("caller mutated")
			}
			duplicate, err := s.Append(ctx, []eventstore.Input{in})
			if err != nil || duplicate[0].Outcome != eventstore.Duplicate {
				t.Fatalf("duplicate %v %v", duplicate, err)
			}
			if _, err = s.Aggregate(ctx, in.Root); err != nil {
				t.Fatal(err)
			}
			history, err := s.History(ctx, in.Root, eventstore.HistoryOptions{IncludeEvents: true})
			if err != nil {
				t.Fatal(err)
			}
			if err = eventstore.Verify(history.Events[0]); err != nil {
				t.Fatal(err)
			}
			if string(history.Context) != string(in.Context) {
				t.Fatal("historical context mutated")
			}
			if err = s.Deliver(ctx, publishFunc(func(_ context.Context, p eventstore.Publication) error { return eventstore.Verify(p.Event) }), 10); err != nil {
				t.Fatal(err)
			}
			block, err := s.BuildBlock(ctx, 1, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = eventstore.VerifyBlock(ctx, block, nil, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFrameworkEventContextAuthority(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t, kind)
			s.Local = true
			identity := transport.Identity{Context: command.Context{"principal": json.RawMessage(`"trusted"`)}, Claims: json.RawMessage(`{"sub":"trusted"}`)}
			f := transport.Framework{Store: s, Network: "local", Authorize: func(*http.Request) (transport.Identity, error) { return identity, nil }}
			handler, err := f.Handler(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			in := input("authority", "authority")
			in.Context = json.RawMessage(`{"principal":"forged"}`)
			in.Transaction.Claims = json.RawMessage(`{"sub":"forged"}`)
			body, err := json.Marshal(map[string]any{"events": []eventstore.Input{in}})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "/events", bytes.NewReader(body)))
			if response.Code != 200 {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
			err = s.Backend.View(context.Background(), func(r eventstore.Reader) error {
				event, err := r.Find(context.Background(), in.Key)
				if err != nil {
					return err
				}
				want, _ := eventstore.Encode(identity.Context)
				if string(event.Input.Context) != string(want) || string(event.Input.Transaction.Claims) != string(identity.Claims) {
					t.Fatal("forged authority persisted")
				}
				return eventstore.Verify(event)
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStoreStreamingRoutes(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t, kind)
			s.Local = true
			ctx := context.Background()
			_, err := s.Append(ctx, []eventstore.Input{input("one", "one"), input("two", "two")})
			if err != nil {
				t.Fatal(err)
			}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signer := audit.ES256{Private: key, Public: &key.PublicKey}
			handler, err := (transport.Framework{BlockSigner: signer, Store: s, Network: "local", Authorize: func(*http.Request) (transport.Identity, error) { return transport.Identity{}, nil }}).Handler(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				path  string
				lines int
			}{
				{"/roots?service=test&domain=counter", 2}, {"/aggregates?service=test&domain=counter", 2}, {"/aggregates?service=test&domain=counter&root=missing", 0},
			} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("GET", tc.path, nil))
				if response.Code != 200 || bytes.Count(response.Body.Bytes(), []byte("\n")) != tc.lines {
					t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
				}
			}
			root := input("one", "one").Root
			server := httptest.NewServer(handler)
			defer server.Close()
			wire, err := rpc.New(server.URL, rpc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			client := transport.StoreClient{RPC: wire}
			block, err := client.BuildBlock(ctx, 2, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = eventstore.VerifyBlock(ctx, block, nil, signer); err != nil {
				t.Fatal(err)
			}
			count, err := client.Count(ctx, root, nil)
			if err != nil || count != 1 {
				t.Fatalf("RPC count %d %v", count, err)
			}
			aggregate, err := client.Aggregate(ctx, root, nil)
			if err != nil || aggregate.Version != 0 {
				t.Fatalf("RPC aggregate %v %v", aggregate, err)
			}
			history, err := client.History(ctx, root, eventstore.HistoryOptions{IncludeEvents: true}, nil)
			if err != nil || len(history.Events) != 1 {
				t.Fatalf("RPC history %v", err)
			}
			streamed := 0
			err = client.Aggregates(ctx, "test", "counter", "", nil, func(context.Context, eventstore.Aggregate) error { streamed++; return nil })
			if err != nil || streamed != 2 {
				t.Fatalf("RPC stream %d %v", streamed, err)
			}
			for _, tc := range []struct {
				path string
				body any
			}{
				{"/count", root}, {"/history", map[string]any{"root": root, "includeEvents": true}},
			} {
				body, _ := json.Marshal(tc.body)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest("POST", tc.path, bytes.NewReader(body)))
				if response.Code != 200 {
					t.Fatalf("%s: %d %s", tc.path, response.Code, response.Body.String())
				}
			}
			stopped := errors.New("consumer stopped")
			calls := 0
			err = s.StreamAggregates(ctx, "local", "test", "counter", "", func(eventstore.Aggregate) error { calls++; return stopped })
			if !errors.Is(err, stopped) || calls != 1 {
				t.Fatalf("consumer error lost or repeated: %d %v", calls, err)
			}
			cancelled, cancel := context.WithCancel(ctx)
			calls = 0
			err = s.StreamAggregates(cancelled, "local", "test", "counter", "", func(eventstore.Aggregate) error { calls++; cancel(); return nil })
			cancel()
			if !errors.Is(err, context.Canceled) || calls != 1 {
				t.Fatalf("cancelled stream: %d %v", calls, err)
			}

		})
	}
}

func TestOversizeEventRollsBackBeforeCommit(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			s := newStore(t, kind)
			for _, local := range []bool{false, true} {
				s.Local = local
				small := input("size", "small")
				large := input("size", "large")
				large.Context, _ = json.Marshal(map[string]string{"large": string(bytes.Repeat([]byte("a"), eventstore.MaxPublicationBytes))})
				if _, err := s.Append(context.Background(), []eventstore.Input{small, large}); !errors.Is(err, eventstore.ErrInvalid) {
					t.Fatalf("local%v: %v", local, err)
				}
				count, err := s.Count(context.Background(), small.Root)
				if err != nil || count != 0 {
					t.Fatalf("partial write %d %v", count, err)
				}
			}
		})
	}
}
