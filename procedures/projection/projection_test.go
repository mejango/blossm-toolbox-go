package projection

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
	"github.com/mejango/blossm-toolbox-go/stores/view"
)

type aggregateFn func(context.Context, eventstore.Root) (eventstore.Aggregate, error)

func (f aggregateFn) Aggregate(ctx context.Context, r eventstore.Root) (eventstore.Aggregate, error) {
	return f(ctx, r)
}

type writerFn func(context.Context, []view.Mutation) ([]view.Result, error)

func (f writerFn) Batch(ctx context.Context, m []view.Mutation) ([]view.Result, error) {
	return f(ctx, m)
}
func testRoot(id string) eventstore.Root {
	return eventstore.Root{Network: "n", Service: "animals", Domain: "bird", ID: id}
}
func raw(s string) json.RawMessage { return json.RawMessage(s) }
func applied(m []view.Mutation) []view.Result {
	out := make([]view.Result, len(m))
	for i, v := range m {
		out[i] = view.Result{ID: v.ID, Version: v.Version, Applied: true}
	}
	return out
}
func simplePlan(_ context.Context, in Input) (Plan, error) {
	return Plan{Mutations: []Mutation{{ID: in.Aggregate.Root.ID, Body: raw(`{"owner":"alice"}`)}}}, nil
}
func engine() Engine {
	return Engine{
		Aggregates: aggregateFn(func(_ context.Context, r eventstore.Root) (eventstore.Aggregate, error) {
			return eventstore.Aggregate{Root: r, Version: 7, State: raw(`{"owner":"alice","sound":"chirp","volume":5}`)}, nil
		}),
		Views:    writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) { return applied(m), nil }),
		Handlers: map[Source]Handler{source(testRoot("root")): simplePlan},
	}
}
func eventRequest(id string) Request {
	r := testRoot(id)
	return Request{Root: r, Event: &Trigger{Root: r, Version: 2, Action: "sang"}}
}

// Port of the retained CLI bird example: current state, not event payload, owns
// the mapped body. Muting suppresses notification, never handler execution.
func TestReferenceCurrentAggregateAndMutedPlay(t *testing.T) {
	for _, mode := range []string{"event", "muted", "manual", "other-source-muted"} {
		t.Run(mode, func(t *testing.T) {
			e := engine()
			request := eventRequest("bird")
			handlers, pushes := 0, 0
			if mode == "manual" {
				request.Event = nil
			}
			if mode == "muted" {
				e.Muted = []Muted{{Source: source(request.Root), Actions: []string{"sang"}}}
			}
			if mode == "other-source-muted" {
				e.Muted = []Muted{{Source: Source{"other", "animals", "bird"}, Actions: []string{"sang"}}}
			}
			e.Handlers[source(request.Root)] = func(_ context.Context, in Input) (Plan, error) {
				handlers++
				wantAction := "sang"
				if mode == "manual" {
					wantAction = ""
				}
				if in.Action != wantAction {
					t.Fatal(in.Action)
				}
				var state struct {
					Sound  string
					Volume int
				}
				if err := json.Unmarshal(in.Aggregate.State, &state); err != nil {
					return Plan{}, err
				}
				sound := state.Sound
				if state.Volume >= 4 {
					sound = strings.ToUpper(sound) + "!"
				}
				body, _ := json.Marshal(map[string]string{"owner": "alice", "latestSound": sound})
				return Plan{Mutations: []Mutation{{ID: "card", Body: body}}}, nil
			}
			e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
				if len(m) != 1 || m[0].Root != request.Root || m[0].Version != 7 || string(m[0].Body) != `{"latestSound":"CHIRP!","owner":"alice"}` {
					t.Fatal(m)
				}
				return applied(m), nil
			})
			e.Push = func(_ context.Context, n Notification) error {
				pushes++
				if n.Root != request.Root || n.Version != 7 || n.ID != "card" {
					t.Fatal(n)
				}
				return nil
			}
			result, err := e.Play(context.Background(), request)
			if err != nil || !result.Committed || handlers != 1 {
				t.Fatal(result, err, handlers)
			}
			wantPush := 1
			if mode == "manual" || mode == "muted" {
				wantPush = 0
			}
			if pushes != wantPush {
				t.Fatal(pushes, wantPush)
			}
		})
	}
}
func TestReferenceNoAppliedViewDoesNotPush(t *testing.T) {
	e := engine()
	e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
		return []view.Result{{ID: m[0].ID, Version: 8, Applied: false}}, nil
	})
	e.Push = func(context.Context, Notification) error { t.Fatal("stale/condition no-op pushed"); return nil }
	r, err := e.Play(context.Background(), eventRequest("card"))
	if err != nil || !r.Committed || r.Rows[0].Applied {
		t.Fatal(r, err)
	}
}
func TestPushFailuresAreAfterCommitAndContinue(t *testing.T) {
	e := engine()
	committed := false
	pushes := 0
	failure := errors.New("push unavailable")
	e.Handlers[source(testRoot("r"))] = func(context.Context, Input) (Plan, error) {
		return Plan{Mutations: []Mutation{{ID: "a", Body: raw(`{}`)}, {ID: "b", Body: raw(`{}`)}}}, nil
	}
	e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
		committed = true
		return applied(m), nil
	})
	e.Push = func(context.Context, Notification) error {
		if !committed {
			t.Fatal("push before durable batch")
		}
		pushes++
		return failure
	}
	r, err := e.Play(context.Background(), eventRequest("r"))
	if err != nil || !r.Committed || pushes != 2 || len(r.PushFailures) != 2 || !errors.Is(r.PushFailures[0].Err, failure) {
		t.Fatal(r, err, pushes)
	}
}
func TestCommitFailureNeverPushes(t *testing.T) {
	for _, failure := range []error{errors.New("write failed"), eventstore.ErrUncertain} {
		e := engine()
		e.Views = writerFn(func(context.Context, []view.Mutation) ([]view.Result, error) { return nil, failure })
		e.Push = func(context.Context, Notification) error {
			t.Fatal("push despite failed/uncertain acknowledgement")
			return nil
		}
		r, err := e.Play(context.Background(), eventRequest("r"))
		if !errors.Is(err, failure) || r.Committed || r.Uncertain != errors.Is(failure, eventstore.ErrUncertain) {
			t.Fatal(r, err)
		}
	}
	e := engine()
	e.Views = writerFn(func(context.Context, []view.Mutation) ([]view.Result, error) { return nil, nil })
	r, err := e.Play(context.Background(), eventRequest("r"))
	if err == nil || !r.Committed {
		t.Fatal("bad result after acknowledgement must not claim rollback", r, err)
	}
}
func TestSourceIdentityAndRevisionValidation(t *testing.T) {
	for _, kind := range []string{"root", "missing", "negative", "unsafe", "future-trigger", "invalid-state", "invalid-trigger-root", "invalid-trigger-version", "invalid-action", "empty-source"} {
		t.Run(kind, func(t *testing.T) {
			e := engine()
			request := eventRequest("r")
			a := eventstore.Aggregate{Root: request.Root, Version: 7, State: raw(`{}`)}
			switch kind {
			case "root":
				a.Root.ID = "other"
			case "missing":
				a.Version = -1
			case "negative":
				a.Version = -2
			case "unsafe":
				a.Version = 9007199254740992
			case "future-trigger":
				request.Event.Version = 8
			case "invalid-state":
				a.State = raw(`{"a":1,"a":2}`)
			case "invalid-trigger-root":
				request.Event.Root.ID = "other"
			case "invalid-trigger-version":
				request.Event.Version = -1
			case "invalid-action":
				request.Event.Action = ""
			case "empty-source":
				request.Root.ID = ""
			}
			e.Aggregates = aggregateFn(func(context.Context, eventstore.Root) (eventstore.Aggregate, error) { return a, nil })
			e.Views = writerFn(func(context.Context, []view.Mutation) ([]view.Result, error) {
				t.Fatal("invalid source reached writer")
				return nil, nil
			})
			if _, err := e.Play(context.Background(), request); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
	e := engine()
	delete(e.Handlers, source(testRoot("r")))
	r, err := e.Play(context.Background(), eventRequest("r"))
	if err != nil || !r.Ignored || r.Committed {
		t.Fatal(r, err)
	}
}
func TestDependenciesAreSerialVersionedAndPreparedBeforeCommit(t *testing.T) {
	e := engine()
	calls := []string{}
	written := []string{}
	pushed := []string{}
	e.Aggregates = aggregateFn(func(_ context.Context, r eventstore.Root) (eventstore.Aggregate, error) {
		return eventstore.Aggregate{Root: r, Version: int64(r.ID[0] - 'a'), State: raw(`{}`)}, nil
	})
	e.Handlers[source(testRoot("a"))] = func(_ context.Context, in Input) (Plan, error) {
		id := in.Aggregate.Root.ID
		calls = append(calls, id)
		p, _ := simplePlan(context.Background(), in)
		if id == "a" {
			p.Dependencies = []Dependency{{Root: testRoot("b"), Flag: "from-parent"}, {Root: testRoot("c")}}
		} else if id == "b" || id == "c" {
			p.Dependencies = []Dependency{{Root: testRoot("d")}}
		}
		if id == "b" && (in.Flag != "from-parent" || in.Action != "") {
			t.Fatal(in)
		}
		return p, nil
	}
	e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
		if !reflect.DeepEqual(calls, []string{"a", "b", "d", "c"}) {
			t.Fatal("writer before graph preparation", calls)
		}
		for _, mutation := range m {
			written = append(written, mutation.ID)
			if mutation.Root.ID != mutation.ID || mutation.Version != int64(mutation.ID[0]-'a') {
				t.Fatal(mutation)
			}
		}
		return applied(m), nil
	})
	e.Push = func(_ context.Context, n Notification) error { pushed = append(pushed, n.ID); return nil }
	request := eventRequest("a")
	request.Event.Version = 0
	r, err := e.Play(context.Background(), request)
	if err != nil || r.Visited != 5 || !reflect.DeepEqual(written, []string{"d", "b", "c", "a"}) || !reflect.DeepEqual(pushed, []string{"a"}) {
		t.Fatal(r, err, written, pushed)
	}
}
func TestDependencyFailureAndBudgetsHaveNoWrites(t *testing.T) {
	for _, kind := range []string{"cycle", "nodes", "mutations", "missing-required", "optional-then-required", "handler-error", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			e := engine()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.Aggregates = aggregateFn(func(_ context.Context, r eventstore.Root) (eventstore.Aggregate, error) {
				if r.ID == "missing" {
					return eventstore.Aggregate{}, eventstore.ErrNotFound
				}
				return eventstore.Aggregate{Root: r, Version: 7, State: raw(`{}`)}, nil
			})
			e.Views = writerFn(func(context.Context, []view.Mutation) ([]view.Result, error) {
				t.Fatal("failed preparation wrote views")
				return nil, nil
			})
			if kind == "nodes" {
				e.Limits.MaxNodes = 1
			}
			if kind == "mutations" {
				e.Limits.MaxMutations = 1
			}
			e.Handlers[source(testRoot("a"))] = func(_ context.Context, in Input) (Plan, error) {
				p, _ := simplePlan(ctx, in)
				switch kind {
				case "cycle":
					p.Dependencies = []Dependency{{Root: testRoot("a"), Flag: "different"}}
				case "nodes":
					p.Dependencies = []Dependency{{Root: testRoot("b")}}
				case "mutations":
					p.Mutations = append(p.Mutations, Mutation{ID: "other", Body: raw(`{}`)})
				case "missing-required":
					p.Dependencies = []Dependency{{Root: testRoot("missing")}}
				case "optional-then-required":
					p.Dependencies = []Dependency{{Root: testRoot("missing"), Optional: true}, {Root: testRoot("missing")}}
				case "handler-error":
					return p, errors.New("handler failed")
				case "canceled":
					cancel()
				}
				return p, nil
			}
			_, err := e.Play(ctx, eventRequest("a"))
			if err == nil {
				t.Fatal("failure ignored")
			}
			if kind == "cycle" && !errors.Is(err, ErrCycle) {
				t.Fatal(err)
			}
			if (kind == "nodes" || kind == "mutations") && !errors.Is(err, ErrBudget) {
				t.Fatal(err)
			}
		})
	}
}
func TestOptionalMissingAndHandlerIsolation(t *testing.T) {
	e := engine()
	sourceState := raw(`{"owner":"alice"}`)
	handlerBody := raw(`{"owner":"alice"}`)
	e.Aggregates = aggregateFn(func(_ context.Context, r eventstore.Root) (eventstore.Aggregate, error) {
		if r.ID == "missing" {
			return eventstore.Aggregate{Root: r, Version: -1, State: raw(`{}`)}, nil
		}
		return eventstore.Aggregate{Root: r, Version: 7, State: sourceState}, nil
	})
	e.Handlers[source(testRoot("a"))] = func(_ context.Context, in Input) (Plan, error) {
		in.Aggregate.State[0] = '['
		in.Aggregate.Root.ID = "forged"
		in.Aggregate.Version = 999
		return Plan{Mutations: []Mutation{{ID: "a", Body: handlerBody}}, Dependencies: []Dependency{{Root: testRoot("missing"), Optional: true}}}, nil
	}
	e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
		if m[0].Root != testRoot("a") || m[0].Version != 7 {
			t.Fatal(m)
		}
		m[0].Body[0] = '['
		return applied(m), nil
	})
	_, err := e.Play(context.Background(), Request{Root: testRoot("a")})
	if err != nil || string(sourceState) != `{"owner":"alice"}` || string(handlerBody) != `{"owner":"alice"}` {
		t.Fatal(err, string(sourceState), string(handlerBody))
	}
}
