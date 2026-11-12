package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

// Read-only fixture, like the retained JS HTTP test's injected event readers.
// Real Framework routing, Store replay/history and RPC framing remain in the path.
type mergeReader struct {
	eventstore.Reader
	events []eventstore.Event
}
type mergeBackend struct {
	eventstore.Backend
	reader *mergeReader
}

func (b mergeBackend) View(ctx context.Context, fn func(eventstore.Reader) error) error {
	return fn(b.reader)
}
func (*mergeReader) Snapshot(context.Context, eventstore.Root) (eventstore.Snapshot, error) {
	return eventstore.Snapshot{}, eventstore.ErrNotFound
}
func (r *mergeReader) Events(ctx context.Context, root eventstore.Root, after int64, fn func(eventstore.Event) error) error {
	for _, e := range r.events {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Input.Root == root && e.Number > after {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}
func (r *mergeReader) Roots(ctx context.Context, fn func(eventstore.Root) error) error {
	roots := map[string]eventstore.Root{}
	for _, e := range r.events {
		roots[e.Input.Root.Key()] = e.Input.Root
	}
	keys := make([]string, 0, len(roots))
	for k := range roots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(roots[k]); err != nil {
			return err
		}
	}
	return nil
}

type mergeReducer struct{}

func (mergeReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return eventstore.Canonical(p)
}
func (mergeReducer) Apply(_ json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}

func mergeHTTP(t *testing.T, domain string) *SelectedStore {
	t.Helper()
	r := &mergeReader{}
	add := func(id string, number int64, rank int, created string) {
		root := eventstore.Root{Network: "local", Service: "core", Domain: domain, ID: id}
		payload, err := eventstore.Encode(map[string]any{"rank": rank, "text": "🦋 }"})
		if err != nil {
			t.Fatal(err)
		}
		e := eventstore.Event{Number: number, Input: eventstore.Input{Root: root, Action: "set", Key: domain + id + created, Created: created, Payload: payload, Context: json.RawMessage(`{"principal":"caller","note":"🦋 }"}`), Transaction: eventstore.Transaction{ID: domain + id, Issued: created}}}
		e.Fingerprint, err = eventstore.Digest(e.Input)
		if err != nil {
			t.Fatal(err)
		}
		e.Hash, err = eventstore.Digest(e)
		if err != nil {
			t.Fatal(err)
		}
		if err = eventstore.Verify(e); err != nil {
			t.Fatal(err)
		}
		r.events = append(r.events, e)
	}
	if domain == "alpha" {
		add("a", 0, 2, "2026-01-01T00:00:00Z")
		add("a", 1, 20, "2026-01-02T00:00:00Z")
		add("extra", 0, 5, "2026-01-01T00:00:00Z")
	} else {
		add("b", 0, 1, "2026-01-01T00:00:00Z")
		add("z", 0, 3, "2026-01-01T00:00:00Z")
	}
	store := &eventstore.Store{Backend: mergeBackend{reader: r}, Local: true, Reducers: map[string]eventstore.Reducer{"core/" + domain: mergeReducer{}}}
	f := Framework{Store: store, Network: "local", Authorize: func(request *http.Request) (Identity, error) {
		if request.Header.Get("Authorization") != "Bearer "+domain {
			return Identity{}, errors.New("wrong source credentials")
		}
		return Identity{}, nil
	}}
	h, err := f.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	client, err := rpc.New(server.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return &SelectedStore{Client: StoreClient{RPC: client}, Headers: http.Header{"Authorization": []string{"Bearer " + domain}}, Network: "local", Service: "core", Domain: domain}
}
func mergeRank(r StoreRecord) int {
	var s struct{ Rank int }
	if err := json.Unmarshal(r.State, &s); err != nil {
		panic(err)
	}
	return s.Rank
}
func mergeLess(a, b StoreRecord) bool { return mergeRank(a) < mergeRank(b) }

func TestStoreMergeHistoricalHTTPSelectors(t *testing.T) {
	a, b := mergeHTTP(t, "alpha"), mergeHTTP(t, "beta")
	cutoff := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	zero := int64(0)
	a.Root = "a"
	a.History = &eventstore.HistoryOptions{ThroughCreated: &cutoff, IncludeEvents: true}
	b.Root = "b"
	b.History = &eventstore.HistoryOptions{ThroughNumber: &zero, IncludeEvents: true}
	var got []StoreRecord
	active := 0
	err := StreamStoreAggregates(context.Background(), []SelectedStore{*a, *b}, StoreMergeOptions{Less: mergeLess, MaxRecords: 2}, func(ctx context.Context, r StoreRecord) error {
		active++
		if active != 1 {
			t.Fatal("concurrent callback")
		}
		defer func() { active-- }()
		time.Sleep(time.Millisecond)
		got = append(got, r)
		return ctx.Err()
	})
	if err != nil || len(got) != 2 || mergeRank(got[0]) != 1 || mergeRank(got[1]) != 2 || got[0].Source != 1 || got[1].Source != 0 {
		t.Fatal(got, err)
	}
	for _, r := range got {
		if !r.Historical || r.Version != 0 || len(r.Events) != 1 || string(r.Context) != `{"note":"🦋 }","principal":"caller"}` {
			t.Fatal("lost historical context", r)
		}
	}
	if string(got[1].State) != `{"rank":2,"text":"🦋 }"}` {
		t.Fatal("Unicode changed", string(got[1].State))
	}
	if a.Root != "a" || b.Root != "b" || !a.History.ThroughCreated.Equal(cutoff) || *b.History.ThroughNumber != 0 || a.Headers.Get("Authorization") != "Bearer alpha" {
		t.Fatal("selection mutated")
	}
	// Historical scans use root enumeration plus the same per-source selectors.
	a.Root = ""
	b.Root = ""
	var roots []string
	if err = StreamStoreAggregates(context.Background(), []SelectedStore{*a, *b}, StoreMergeOptions{}, func(_ context.Context, r StoreRecord) error { roots = append(roots, r.Root.ID); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roots, []string{"a", "extra", "b", "z"}) {
		t.Fatal(roots)
	}
}

func TestStoreMergeCurrentHTTPBudgetAndFailures(t *testing.T) {
	a, b := mergeHTTP(t, "alpha"), mergeHTTP(t, "beta")
	sources := []SelectedStore{*a, *b}
	var got []int
	if err := StreamStoreAggregates(context.Background(), sources, StoreMergeOptions{}, func(_ context.Context, r StoreRecord) error {
		if r.Historical || r.Context != nil || r.Events != nil {
			t.Fatal("current state mislabeled")
		}
		got = append(got, mergeRank(r))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{20, 5, 1, 3}) {
		t.Fatal(got)
	}
	calls := 0
	yield := func(context.Context, StoreRecord) error { calls++; return nil }
	if err := StreamStoreAggregates(context.Background(), sources, StoreMergeOptions{Less: mergeLess, MaxRecords: 3}, yield); !errors.Is(err, ErrStoreMergeBudget) || calls != 0 {
		t.Fatal("partial sorted budget output", calls, err)
	}
	for _, bad := range []SelectedStore{func() SelectedStore {
		x := *b
		x.Headers = http.Header{"Authorization": []string{"Bearer wrong"}}
		return x
	}(), func() SelectedStore { x := *b; x.Network = "foreign"; return x }()} {
		if err := StreamStoreAggregates(context.Background(), []SelectedStore{*a, bad}, StoreMergeOptions{Less: mergeLess, MaxRecords: 10}, yield); err == nil || calls != 0 {
			t.Fatal("failed source emitted sorted prefix", calls, err)
		}
	}
	if err := StreamStoreAggregates(context.Background(), sources, StoreMergeOptions{Less: mergeLess}, yield); !errors.Is(err, eventstore.ErrInvalid) {
		t.Fatal("unbounded sort accepted", err)
	}
	got = nil
	if err := StreamStoreAggregates(context.Background(), sources, StoreMergeOptions{Less: mergeLess, MaxRecords: 4}, func(_ context.Context, r StoreRecord) error { got = append(got, mergeRank(r)); return nil }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []int{1, 3, 5, 20}) {
		t.Fatal("sort assumed per-source order", got)
	}
}

func TestStoreMergeIncrementalCancellationAndCallbackError(t *testing.T) {
	for _, cancelConsumer := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback-error", true: "cancel"}[cancelConsumer], func(t *testing.T) {
			stopped := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(stopped)
				_ = json.NewEncoder(w).Encode(eventstore.Aggregate{Root: eventstore.Root{Network: "local", Service: "core", Domain: "alpha", ID: "first"}, Version: 0, State: json.RawMessage(`{"rank":1}`)})
				_ = http.NewResponseController(w).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()
			var later atomic.Int32
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { later.Add(1); w.WriteHeader(500) }))
			defer second.Close()
			firstRPC, err := rpc.New(server.URL, rpc.Options{Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			secondRPC, err := rpc.New(second.URL, rpc.Options{})
			if err != nil {
				t.Fatal(err)
			}
			sources := []SelectedStore{{Client: StoreClient{RPC: firstRPC}, Network: "local", Service: "core", Domain: "alpha"}, {Client: StoreClient{RPC: secondRPC}, Network: "local", Service: "core", Domain: "beta"}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cause := errors.New("consumer failed")
			calls := 0
			err = StreamStoreAggregates(ctx, sources, StoreMergeOptions{}, func(callbackCtx context.Context, r StoreRecord) error {
				calls++
				if later.Load() != 0 {
					t.Error("second source started before first callback")
				}
				if cancelConsumer {
					cancel()
					return nil
				}
				return cause
			})
			expected := cause
			if cancelConsumer {
				expected = context.Canceled
			}
			if !errors.Is(err, expected) || calls != 1 || later.Load() != 0 {
				t.Fatal(calls, later.Load(), err)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("source body not closed")
			}
		})
	}
}
