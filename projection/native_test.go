//go:build integration

package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"blossm.local/backend/eventstore"
	mongostore "blossm.local/backend/storage/mongo"
	"blossm.local/backend/storage/postgres"
	"blossm.local/backend/view"
	"github.com/jackc/pgx/v5"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMain(m *testing.M) {
	for _, name := range []string{"POSTGRES_URL", "MONGODB_URI"} {
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
			fmt.Fprintln(os.Stderr, name+" must name a disposable localhost database; no skipped integration tests")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}
func nativeBackend(t *testing.T, kind string) (eventstore.Backend, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	name := fmt.Sprintf("projection_test_%d", time.Now().UnixNano())
	if kind == "postgres" {
		b, err := postgres.Open(ctx, os.Getenv("POSTGRES_URL"), name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			c, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			b.Close(c)
			conn, err := pgx.Connect(c, os.Getenv("POSTGRES_URL"))
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close(c)
			if _, err = conn.Exec(c, "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
				t.Error(err)
			}
		})
		return b, ctx
	}
	b, err := mongostore.Open(ctx, mongostore.Config{URI: os.Getenv("MONGODB_URI"), Database: name, Namespace: "projection", MaxAttempts: 30})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := b.Close(c); err != nil {
			t.Error(err)
		}
		client, err := driver.Connect(options.Client().ApplyURI(os.Getenv("MONGODB_URI")))
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Disconnect(c)
		if err = client.Database(name).Drop(c); err != nil {
			t.Error(err)
		}
	})
	return b, ctx
}

type replaceReducer struct{}

func (replaceReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return eventstore.Canonical(p)
}
func (replaceReducer) Apply(_ json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}
func nativeOwners(b eventstore.Backend) (*eventstore.Store, *view.Store, Engine) {
	events := &eventstore.Store{Backend: b, Local: true, Reducers: map[string]eventstore.Reducer{"animals/bird": replaceReducer{}}}
	views := &view.Store{Backend: b, Scope: view.Scope{Network: "n", Service: "animals", Domain: "bird", Owner: []eventstore.Predicate{{Path: "owner", Value: raw(`"alice"`)}}}}
	e := Engine{Aggregates: events, Views: views, Handlers: map[Source]Handler{}}
	return events, views, e
}
func appendState(t *testing.T, ctx context.Context, s *eventstore.Store, id, key, body string) Request {
	t.Helper()
	root := testRoot(id)
	receipt, err := s.Append(ctx, []eventstore.Input{{Root: root, Key: key, Action: "changed", Created: "2026-11-12T00:00:00Z", Payload: raw(body), Transaction: eventstore.Transaction{ID: key, Issued: "2026-11-12T00:00:00Z"}}})
	if err != nil {
		t.Fatal(err)
	}
	return Request{Root: root, Event: &Trigger{Root: root, Version: receipt[0].Event.Number, Action: "changed"}}
}
func TestNativeProjectionOutOfOrderAndTombstones(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			for _, deleted := range []bool{false, true} {
				t.Run(fmt.Sprintf("deleted-%v", deleted), func(t *testing.T) {
					backend, ctx := nativeBackend(t, kind)
					events, views, e := nativeOwners(backend)
					first := appendState(t, ctx, events, "bird", "old", `{"owner":"alice","value":"old"}`)
					started, release := make(chan struct{}), make(chan struct{})
					var releaseOnce sync.Once
					t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
					e.Handlers[source(first.Root)] = func(ctx context.Context, in Input) (Plan, error) {
						if in.Aggregate.Version == 0 {
							close(started)
							select {
							case <-release:
							case <-ctx.Done():
								return Plan{}, ctx.Err()
							}
						}
						var state struct{ Deleted bool }
						if err := json.Unmarshal(in.Aggregate.State, &state); err != nil {
							return Plan{}, err
						}
						return Plan{Mutations: []Mutation{{ID: "card", Body: in.Aggregate.State, Tombstone: state.Deleted}}}, nil
					}
					var pushes atomic.Int64
					e.Push = func(_ context.Context, n Notification) error {
						pushes.Add(1)
						if n.Version != 1 || n.Deleted != deleted {
							t.Error("bad postcommit notification", n)
						}
						return nil
					}
					type completion struct {
						result Result
						err    error
					}
					old := make(chan completion, 1)
					go func() { r, err := e.Play(ctx, first); old <- completion{r, err} }()
					select {
					case <-started:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					newer := appendState(t, ctx, events, "bird", "new", fmt.Sprintf(`{"owner":"alice","value":"new","deleted":%v}`, deleted))
					fresh, err := e.Play(ctx, newer)
					if err != nil || !fresh.Committed || !fresh.Rows[0].Applied {
						t.Fatal(fresh, err)
					}
					releaseOnce.Do(func() { close(release) })
					var stale completion
					select {
					case stale = <-old:
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
					if stale.err != nil || !stale.result.Committed || stale.result.Rows[0].Applied || pushes.Load() != 1 {
						t.Fatal("stale play overwrote/re-pushed", stale, pushes.Load())
					}
					p, err := views.Get(ctx, "card")
					if deleted {
						if !errors.Is(err, eventstore.ErrNotFound) {
							t.Fatal(p, err)
						}
					} else if err != nil || p.Version != 1 {
						t.Fatal(p, err)
					}
					if err = backend.View(ctx, func(r eventstore.Reader) error {
						p, err := r.Projection(ctx, "card")
						if err == nil && (p.Version != 1 || p.Deleted != deleted) {
							t.Fatal(p)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}
func TestNativeProjectionConditionsFiltersAndAtomicFailure(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			backend, ctx := nativeBackend(t, kind)
			events, views, e := nativeOwners(backend)
			e.Handlers[source(testRoot("r"))] = func(_ context.Context, in Input) (Plan, error) {
				if in.Aggregate.Root.ID == "bad-batch" {
					return Plan{Mutations: []Mutation{{ID: "good", Body: raw(`{"owner":"alice"}`)}, {ID: "bad", Body: raw(`{"owner":"bob"}`)}}}, nil
				}
				if in.Aggregate.Version == 0 {
					return Plan{Mutations: []Mutation{{ID: "filtered", Body: in.Aggregate.State}}}, nil
				}
				expected := int64(0)
				condition := raw(`"active"`)
				if in.Aggregate.Version == 2 {
					condition = raw(`"closed"`)
					expected = 1
				}
				return Plan{Mutations: []Mutation{{ID: "filtered", Expected: &expected, If: []eventstore.Predicate{{Path: "status", Value: condition}}, Operations: []view.Operation{{Kind: view.Set, Path: "items.$[item].value", Value: raw(`"new"`), Filters: map[string][]eventstore.Predicate{"item": {{Path: "id", Value: raw(`"first"`)}}}}}}}}, nil
			}
			body := `{"owner":"alice","status":"active","items":[{"id":"first","value":"old"},{"id":"second","value":"old"}]}`
			pushFailure := errors.New("notification provider unavailable")
			e.Push = func(ctx context.Context, n Notification) error {
				if _, err := views.Get(ctx, n.ID); err != nil {
					t.Error("notification before readable commit", err)
				}
				return pushFailure
			}
			request := appendState(t, ctx, events, "r", "initial", body)
			initial, err := e.Play(ctx, request)
			if err != nil || !initial.Committed || len(initial.PushFailures) != 1 || !errors.Is(initial.PushFailures[0].Err, pushFailure) {
				t.Fatal(initial, err)
			}
			request = appendState(t, ctx, events, "r", "patch", body)
			result, err := e.Play(ctx, request)
			if err != nil || !result.Rows[0].Applied {
				t.Fatal(result, err)
			}
			p, err := views.Get(ctx, "filtered")
			if err != nil {
				t.Fatal(err)
			}
			matches, err := eventstore.Matches(p.Body, []eventstore.Predicate{{Path: "items.value", Value: raw(`"new"`)}, {Path: "items.value", Value: raw(`"old"`)}})
			if err != nil || !matches {
				t.Fatal(string(p.Body), err)
			}
			request = appendState(t, ctx, events, "r", "conditional-noop", body)
			result, err = e.Play(ctx, request)
			if err != nil || result.Rows[0].Applied {
				t.Fatal(result, err)
			}
			request = appendState(t, ctx, events, "bad-batch", "bad-batch", `{"owner":"alice"}`)
			result, err = e.Play(ctx, request)
			if err == nil || result.Committed {
				t.Fatal("batch claimed success", result, err)
			}
			if _, err = views.Get(ctx, "good"); !errors.Is(err, eventstore.ErrNotFound) {
				t.Fatal("atomic batch left partial view", err)
			}
		})
	}
}
func TestNativeReplayBuffersBeforeProducerEffects(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			backend, ctx := nativeBackend(t, kind)
			events, _, e := nativeOwners(backend)
			e.Handlers[source(testRoot("a"))] = simplePlan
			for _, id := range []string{"a", "b", "c"} {
				appendState(t, ctx, events, id, id, `{"owner":"alice"}`)
			}
			if err := backend.Update(ctx, func(tx eventstore.Tx) error { return tx.LockRoots(ctx, []eventstore.Root{testRoot("empty")}) }); err != nil {
				t.Fatal(err)
			}
			e.Push = func(context.Context, Notification) error { t.Fatal("scheduled replay pushed"); return nil }
			s := Scheduler{Roots: BackendRoots{Backend: backend}, Producer: producerFn(func(ctx context.Context, r Request) error { _, err := e.Play(ctx, r); return err })}
			first, err := s.Replay(ctx, source(testRoot("a")), "run-1", "", 2)
			if err != nil || first.Enqueued != 2 || first.Complete {
				t.Fatal(first, err)
			}
			last, err := s.Replay(ctx, source(testRoot("a")), "run-1", first.After, 2)
			if err != nil || last.Enqueued != 1 || !last.Complete {
				t.Fatal(last, err)
			}
		})
	}
}
