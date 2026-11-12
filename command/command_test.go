package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/rpc"
)

// The fixture supplies the transaction primitives used by the real Store.Append;
// command tests exercise its normalization, deduplication, replay and commit gate.
type memoryBackend struct {
	mutex   sync.Mutex
	records []eventstore.Event
	counts  map[string]int64
}
type memoryTx struct {
	eventstore.Tx
	records []eventstore.Event
	counts  map[string]int64
}

func (b *memoryBackend) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	tx := &memoryTx{records: append([]eventstore.Event(nil), b.records...), counts: map[string]int64{}}
	for k, v := range b.counts {
		tx.counts[k] = v
	}
	if err := fn(tx); err != nil {
		return err
	}
	b.records = tx.records
	b.counts = tx.counts
	return nil
}
func (b *memoryBackend) View(ctx context.Context, fn func(eventstore.Reader) error) error {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return fn(&memoryTx{records: b.records, counts: b.counts})
}
func (*memoryBackend) Close(context.Context) error                   { return nil }
func (*memoryTx) LockRoots(context.Context, []eventstore.Root) error { return nil }
func (m *memoryTx) Find(_ context.Context, key string) (eventstore.Event, error) {
	for _, e := range m.records {
		if e.Input.Key == key {
			return e, nil
		}
	}
	return eventstore.Event{}, eventstore.ErrNotFound
}
func (m *memoryTx) Count(_ context.Context, r eventstore.Root) (int64, error) {
	return m.counts[r.Key()], nil
}
func (m *memoryTx) PutCount(_ context.Context, r eventstore.Root, n int64) error {
	m.counts[r.Key()] = n
	return nil
}
func (m *memoryTx) PutEvent(_ context.Context, e eventstore.Event) error {
	m.records = append(m.records, e)
	return nil
}
func (*memoryTx) Snapshot(context.Context, eventstore.Root) (eventstore.Snapshot, error) {
	return eventstore.Snapshot{}, eventstore.ErrNotFound
}
func (m *memoryTx) Events(_ context.Context, r eventstore.Root, after int64, fn func(eventstore.Event) error) error {
	for _, e := range m.records {
		if e.Input.Root == r && e.Number > after {
			if err := fn(e); err != nil {
				return err
			}
		}
	}
	return nil
}

type reducer struct{}

func (reducer) Normalize(action string, p json.RawMessage) (json.RawMessage, error) {
	if action != "set" {
		return nil, errors.New("invalid action")
	}
	return eventstore.Canonical(p)
}
func (reducer) Apply(_ json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}
func testStore() *eventstore.Store {
	return &eventstore.Store{Backend: &memoryBackend{}, Reducers: map[string]eventstore.Reducer{"core/example": reducer{}}, Local: true}
}
func req() Request {
	return Request{ID: "command-1", Issued: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"value":1}`), Root: &eventstore.Root{Network: "local", Service: "core", Domain: "example", ID: "one"}, Context: Context{"principal": json.RawMessage(`"owner"`)}}
}
func basic() Procedure {
	return Procedure{Store: testStore(), RequiresRoot: true, RequiredContexts: []string{"principal"}, Main: func(_ context.Context, r Request) (Result, error) {
		return Result{Events: []eventstore.Input{{Action: "set", Payload: r.Payload}}}, nil
	}}
}

func TestValidationOrderAndTrace(t *testing.T) {
	p := basic()
	order := ""
	p.Validate = func(_ context.Context, r Request) error { order += "v"; return nil }
	p.Fill = func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) { order += "f"; return raw, nil }
	p.Normalize = func(_ context.Context, raw json.RawMessage) (json.RawMessage, error) { order += "n"; return raw, nil }
	old := p.Main
	p.Main = func(ctx context.Context, r Request) (Result, error) { order += "m"; return old(ctx, r) }
	p.Trace = &eventstore.Trace{Network: "local", Host: "host", Procedure: "command", Hash: "revision"}
	bad := req()
	bad.Context = nil
	if _, err := p.Execute(context.Background(), bad); !errors.Is(err, ErrForbidden) || order != "" {
		t.Fatal("missing context reached execution", err, order)
	}
	bad = req()
	bad.Root = nil
	if _, err := p.Execute(context.Background(), bad); !errors.Is(err, ErrInvalid) || order != "" {
		t.Fatal("missing root reached execution", err, order)
	}
	out, err := p.Execute(context.Background(), req())
	if err != nil || order != "vfnm" {
		t.Fatal(err, order)
	}
	input := out.Receipts[0].Event.Input
	if input.Created != req().Issued || input.Transaction.ID != req().ID || len(input.Transaction.Path) != 1 || input.Context == nil {
		t.Fatal("lost command envelope", input)
	}
}

func TestFreshCommitDuplicateAndCallbackContributions(t *testing.T) {
	p := basic()
	callbacks := 0
	p.Main = func(_ context.Context, r Request) (Result, error) {
		zero := int64(0)
		return Result{Events: []eventstore.Input{{Action: "set", Payload: r.Payload, Expected: &zero}}, Response: Contribution{Body: map[string]json.RawMessage{"nested": json.RawMessage(`{"old":1}`)}, Tokens: []json.RawMessage{json.RawMessage(`"a"`)}}, Then: func(_ context.Context, fresh []Receipt) (Contribution, error) {
			if len(fresh) != 1 {
				t.Fatal("missing outcome")
			}
			if fresh[0].Outcome == eventstore.Committed {
				callbacks++
			}
			return Contribution{Body: map[string]json.RawMessage{"nested": json.RawMessage(`{"new":2}`)}, Tokens: []json.RawMessage{json.RawMessage(`"b"`)}, StatusCode: 201}, nil
		}}, nil
	}
	first, err := p.Execute(context.Background(), req())
	if err != nil || first.Receipts[0].Outcome != eventstore.Committed || callbacks != 1 || first.StatusCode != 201 || string(first.Body["nested"]) != `{"new":2}` || len(first.Tokens) != 2 {
		t.Fatal(first, err)
	}
	replay, err := p.Execute(context.Background(), req())
	if err != nil || replay.Receipts[0].Outcome != eventstore.Duplicate || callbacks != 1 || string(replay.Body["nested"]) != `{"new":2}` || replay.StatusCode != 201 {
		t.Fatal("duplicate reran effect", replay, err)
	}
	changed := req()
	changed.Payload = json.RawMessage(`{"value":2}`)
	if _, err = p.Execute(context.Background(), changed); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal("changed retry accepted", err)
	}
}

func TestCallbackFailurePreservesCommitAndIsNotDurable(t *testing.T) {
	p := basic()
	calls := 0
	cause := errors.New("delivery failed")
	p.Main = func(_ context.Context, r Request) (Result, error) {
		return Result{Events: []eventstore.Input{{Action: "set", Payload: r.Payload}}, Then: func(_ context.Context, outcomes []Receipt) (Contribution, error) {
			if outcomes[0].Outcome != eventstore.Committed {
				return Contribution{}, nil
			}
			calls++
			return Contribution{}, cause
		}}, nil
	}
	out, err := p.Execute(context.Background(), req())
	var callbackErr *CallbackError
	if !errors.As(err, &callbackErr) || !errors.Is(err, cause) || len(out.Receipts) != 1 || out.Receipts[0].Outcome != eventstore.Committed {
		t.Fatal("commit evidence lost", out, err)
	}
	out, err = p.Execute(context.Background(), req())
	if err != nil || out.Receipts[0].Outcome != eventstore.Duplicate || calls != 1 {
		t.Fatal("callback incorrectly recovered as durable action", out, err)
	}
}

type queue struct {
	batch      Submission
	acceptance Acceptance
	err        error
	calls      int
}

func (q *queue) Enqueue(_ context.Context, s Submission) (Acceptance, error) {
	q.batch = s
	q.calls++
	return q.acceptance, q.err
}
func TestQueuedAcceptanceAndConsume(t *testing.T) {
	p := basic()
	q := &queue{acceptance: Acceptance{ID: "provider-message"}}
	p.Queue = q
	in := req()
	in.Async = true
	callback := false
	old := p.Main
	p.Main = func(ctx context.Context, in Request) (Result, error) {
		r, err := old(ctx, in)
		r.Then = func(context.Context, []Receipt) (Contribution, error) {
			callback = true
			return Contribution{}, nil
		}
		return r, err
	}
	out, err := p.Execute(context.Background(), in)
	if err != nil || out.Receipts[0].Outcome != eventstore.Queued || out.Receipts[0].Event != nil || !callback || out.Acceptance.ID != "provider-message" {
		t.Fatal(out, err)
	}
	receipts, err := Consume(context.Background(), p.Store, q.batch)
	if err != nil || receipts[0].Outcome != eventstore.Committed {
		t.Fatal(receipts, err)
	}
	receipts, err = Consume(context.Background(), p.Store, q.batch)
	if err != nil || receipts[0].Outcome != eventstore.Duplicate {
		t.Fatal(receipts, err)
	}
	q.acceptance.ID = ""
	out, err = p.Execute(context.Background(), in)
	if !errors.Is(err, ErrQueueUncertain) || len(out.Receipts) != 0 {
		t.Fatal("missing ack became queued", out, err)
	}
	q.err = errors.New("provider connection reset")
	out, err = p.Execute(context.Background(), in)
	if !errors.Is(err, ErrQueueUncertain) || len(out.Receipts) != 0 {
		t.Fatal(out, err)
	}
}

func TestConditionalAndLocalCommandsBypassQueue(t *testing.T) {
	for _, conditional := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "conditional"}[conditional], func(t *testing.T) {
			p := basic()
			q := &queue{err: errors.New("queue must not be called")}
			p.Queue = q
			p.Local = !conditional
			old := p.Main
			if conditional {
				p.Main = func(ctx context.Context, in Request) (Result, error) {
					r, err := old(ctx, in)
					zero := int64(0)
					r.Events[0].Expected = &zero
					return r, err
				}
			}
			in := req()
			in.Async = true
			out, err := p.Execute(context.Background(), in)
			if err != nil || out.Receipts[0].Outcome != eventstore.Committed || q.calls != 0 {
				t.Fatal(out, err, q.calls)
			}
		})
	}
}

func TestCommandThroughActualHTTPRPC(t *testing.T) {
	p := basic()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer test" {
			http.Error(w, "unauthorized", 401)
			return
		}
		var in Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			http.Error(w, "invalid", 400)
			return
		}
		in.Context = Context{"principal": json.RawMessage(`"trusted-owner"`)} // authenticated transport owns context
		out, err := p.Execute(r.Context(), in)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(out.StatusCode)
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer server.Close()
	client, err := rpc.New(server.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []eventstore.Outcome{eventstore.Committed, eventstore.Duplicate} {
		wire, err := client.Do(context.Background(), http.MethodPost, "/command", nil, req(), http.Header{"Authorization": []string{"Bearer test"}})
		if err != nil {
			t.Fatal(err)
		}
		var out Response
		if err = json.Unmarshal(wire.Body, &out); err != nil || len(out.Receipts) != 1 || out.Receipts[0].Outcome != want {
			t.Fatal(i, string(wire.Body), err)
		}
	}
}

func TestMixedBatchCallbackGetsAllOutcomes(t *testing.T) {
	p := basic()
	count := 1
	var received []Receipt
	p.Main = func(_ context.Context, r Request) (Result, error) {
		events := []eventstore.Input{{Action: "set", Payload: r.Payload}}
		if count == 2 {
			events = append(events, eventstore.Input{Action: "set", Payload: r.Payload})
		}
		return Result{Events: events, Then: func(_ context.Context, fresh []Receipt) (Contribution, error) {
			received = fresh
			return Contribution{}, nil
		}}, nil
	}
	if _, err := p.Execute(context.Background(), req()); err != nil {
		t.Fatal(err)
	}
	count = 2
	out, err := p.Execute(context.Background(), req())
	if err != nil || len(out.Receipts) != 2 || out.Receipts[0].Outcome != eventstore.Duplicate || out.Receipts[1].Outcome != eventstore.Committed || len(received) != 2 || received[0].Outcome != eventstore.Duplicate || received[1].Outcome != eventstore.Committed || received[0].Event.Number != 0 || received[1].Event.Number != 1 {
		t.Fatal(out, received, err)
	}
}
func TestTokenOnlyResponseHasContent(t *testing.T) {
	p := basic()
	p.Main = func(context.Context, Request) (Result, error) {
		return Result{Response: Contribution{Tokens: []json.RawMessage{json.RawMessage(`"token"`)}}}, nil
	}
	out, err := p.Execute(context.Background(), req())
	if err != nil || out.StatusCode != 200 {
		t.Fatal(out, err)
	}
}

func TestCommandIDCannotBeRetargeted(t *testing.T) {
	p := basic()
	in := req()
	if _, err := p.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	other := *in.Root
	other.ID = "other"
	in.Root = &other
	if _, err := p.Execute(context.Background(), in); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal("same command ID accepted a different target", err)
	}
}

func TestConditionalConflictDoesNotInvokeCallback(t *testing.T) {
	p := basic()
	calls := 0
	p.Main = func(_ context.Context, in Request) (Result, error) {
		zero := int64(0)
		return Result{Events: []eventstore.Input{{Action: "set", Payload: in.Payload, Expected: &zero}}, Then: func(context.Context, []Receipt) (Contribution, error) { calls++; return Contribution{}, nil }}, nil
	}
	in := req()
	if _, err := p.Execute(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.ID = "competing-command"
	out, err := p.Execute(context.Background(), in)
	if !errors.Is(err, eventstore.ErrConflict) || calls != 1 || len(out.Receipts) != 0 {
		t.Fatal("failed condition leaked effects", out, err, calls)
	}
}

func TestCallbackMutationCannotChangeCommitEvidence(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fail], func(t *testing.T) {
			ctx := context.Background()
			store := testStore()
			p := basic()
			p.Store = store
			cause := errors.New("callback failed after mutation")
			var original []byte
			p.Main = func(_ context.Context, r Request) (Result, error) {
				zero := int64(0)
				return Result{Events: []eventstore.Input{{Action: "set", Payload: r.Payload, Expected: &zero}}, Then: func(_ context.Context, fresh []Receipt) (Contribution, error) {
					if len(fresh) != 1 {
						t.Fatal("expected one fresh receipt")
					}
					var err error
					original, err = json.Marshal(fresh[0].Event)
					if err != nil {
						t.Fatal(err)
					}
					input := &fresh[0].Event.Input
					input.Payload[0] = '!'
					input.Context[0] = '!'
					input.Transaction.Claims[0] = '!'
					input.Transaction.Path[0].Host = "mutated"
					*input.Expected = 99
					fresh[0].Input.Payload[0] = '!'
					fresh[0].Input.Context[0] = '!'
					fresh[0].Input.Transaction.Claims[0] = '!'
					fresh[0].Input.Transaction.Path[0].Host = "mutated-input"
					*fresh[0].Input.Expected = 99
					if fail {
						return Contribution{}, cause
					}
					return Contribution{}, nil
				}}, nil
			}
			in := req()
			in.Claims = json.RawMessage(`{"sub":"owner"}`)
			in.Transaction.Path = []eventstore.Trace{{Network: "local", Host: "host", Procedure: "command", Hash: "revision"}}
			out, err := p.Execute(ctx, in)
			var callbackErr *CallbackError
			if fail {
				if !errors.As(err, &callbackErr) || !errors.Is(err, cause) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(out.Receipts) != 1 || out.Receipts[0].Outcome != eventstore.Committed || out.Receipts[0].Event == nil {
				t.Fatal("missing commit evidence", out)
			}
			check := func(event eventstore.Event) {
				t.Helper()
				if err := eventstore.Verify(event); err != nil {
					t.Fatal("callback corrupted hash", err)
				}
				raw, err := json.Marshal(event)
				if err != nil || string(raw) != string(original) {
					t.Fatal("callback changed committed bytes", err)
				}
			}
			check(*out.Receipts[0].Event)
			if !json.Valid(out.Receipts[0].Input.Payload) || !json.Valid(out.Receipts[0].Input.Context) || !json.Valid(out.Receipts[0].Input.Transaction.Claims) || *out.Receipts[0].Input.Expected != 0 || out.Receipts[0].Input.Transaction.Path[0].Host != "host" {
				t.Fatal("callback changed response input")
			}
			if err := store.Backend.View(ctx, func(r eventstore.Reader) error {
				event, err := r.Find(ctx, out.Receipts[0].Event.Input.Key)
				if err != nil {
					return err
				}
				check(event)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			aggregate, err := store.Aggregate(ctx, *in.Root)
			if err != nil || aggregate.Version != 0 || string(aggregate.State) != string(in.Payload) {
				t.Fatal("persisted replay changed", aggregate, err)
			}
		})
	}
}

func TestThenEmptyAndQueuedOutcomes(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "queued"}[queued], func(t *testing.T) {
			p := basic()
			p.Queue = &queue{acceptance: Acceptance{ID: "accepted"}}
			in := req()
			in.Async = true
			calls := 0
			p.Main = func(_ context.Context, r Request) (Result, error) {
				var events []eventstore.Input
				if queued {
					events = []eventstore.Input{{Action: "set", Payload: r.Payload}}
				}
				return Result{Events: events, Then: func(_ context.Context, outcomes []Receipt) (Contribution, error) {
					calls++
					if outcomes == nil {
						t.Fatal("nil outcomes instead of empty slice")
					}
					if queued {
						if len(outcomes) != 1 || outcomes[0].Outcome != eventstore.Queued || outcomes[0].Event != nil {
							t.Fatal(outcomes)
						}
						outcomes[0].Input.Payload[0] = '!'
						outcomes[0].Input.Context[0] = '!'
					} else if len(outcomes) != 0 {
						t.Fatal(outcomes)
					}
					return Contribution{Body: map[string]json.RawMessage{"response": json.RawMessage(`"recoverable"`)}}, nil
				}}, nil
			}
			for i := 0; i < 2; i++ {
				out, err := p.Execute(context.Background(), in)
				if err != nil || string(out.Body["response"]) != `"recoverable"` {
					t.Fatal(out, err)
				}
				if queued {
					if !json.Valid(out.Receipts[0].Input.Payload) || !json.Valid(out.Receipts[0].Input.Context) {
						t.Fatal("queued callback aliases response")
					}
				} else if out.StatusCode != 200 {
					t.Fatal(out.StatusCode)
				}
			}
			if calls != 2 {
				t.Fatal("Then not called on each outcome", calls)
			}
			if queued {
				p.Queue.(*queue).err = errors.New("acceptance unknown")
				if _, err := p.Execute(context.Background(), in); !errors.Is(err, ErrQueueUncertain) || calls != 2 {
					t.Fatal("unconfirmed queue invoked Then", err, calls)
				}
			}
		})
	}
}

func TestThenFailurePreservesQueuedAcceptance(t *testing.T) {
	p := basic()
	p.Queue = &queue{acceptance: Acceptance{ID: "accepted"}}
	old := p.Main
	cause := errors.New("response failed")
	p.Main = func(ctx context.Context, in Request) (Result, error) {
		r, e := old(ctx, in)
		r.Then = func(context.Context, []Receipt) (Contribution, error) { return Contribution{}, cause }
		return r, e
	}
	in := req()
	in.Async = true
	out, err := p.Execute(context.Background(), in)
	var callback *CallbackError
	if !errors.As(err, &callback) || !errors.Is(err, cause) || out.Acceptance == nil || out.Acceptance.ID != "accepted" || out.Receipts[0].Outcome != eventstore.Queued {
		t.Fatal(out, err)
	}
}

func TestGeneratedKeysAreActorScoped(t *testing.T) {
	ctx := context.Background()
	p := basic()
	a := req()
	a.Context = Context{"principal": json.RawMessage(`{"network":"local","id":"alice"}`)}
	first, err := p.Execute(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	b := req()
	b.Context = Context{"principal": json.RawMessage(`{"network":"local","id":"bob"}`)}
	second, err := p.Execute(ctx, b)
	if err != nil || second.Receipts[0].Outcome != eventstore.Committed || first.Receipts[0].Event.Input.Key == second.Receipts[0].Event.Input.Key {
		t.Fatal("another actor reserved this request ID", second, err)
	}
	// Reordering trusted principal JSON cannot create a new namespace.
	a.Context["principal"] = json.RawMessage(`{ "id":"alice", "network":"local" }`)
	duplicate, err := p.Execute(ctx, a)
	if err != nil || duplicate.Receipts[0].Outcome != eventstore.Duplicate {
		t.Fatal("principal JSON formatting changed identity", duplicate, err)
	}
	other := *a.Root
	other.ID = "other"
	a.Root = &other
	if _, err = p.Execute(ctx, a); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal("actor retarget escaped original request", err)
	}
}

func TestGeneratedKeysUseStableClaimsWhenPrincipalAbsent(t *testing.T) {
	ctx := context.Background()
	p := basic()
	p.RequiredContexts = nil
	in := req()
	in.Context = nil
	in.Claims = json.RawMessage(`{"iss":"issuer","aud":["service"],"sub":"alice","exp":100,"iat":10,"nbf":10,"jti":"first","role":"reader"}`)
	first, err := p.Execute(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	in.Claims = json.RawMessage(`{"role":"reader","sub":"alice","aud":["service"],"iss":"issuer","exp":200,"iat":20,"nbf":20,"jti":"refreshed"}`)
	repeated, err := p.Execute(ctx, in)
	if err != nil || repeated.Receipts[0].Outcome != eventstore.Duplicate || repeated.Receipts[0].Event.Hash != first.Receipts[0].Event.Hash {
		t.Fatal("token refresh changed request identity", repeated, err)
	}
	// Nonidentity claims remain fingerprint-bound, not a way to allocate another key.
	in.Claims = json.RawMessage(`{"iss":"issuer","aud":["service"],"sub":"alice","role":"admin"}`)
	if _, err = p.Execute(ctx, in); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal("authority change escaped fingerprint", err)
	}
	for _, claims := range []string{
		`{"iss":"issuer","aud":["service"],"sub":"bob"}`,
		`{"iss":"other-issuer","aud":["service"],"sub":"alice"}`,
		`{"iss":"issuer","aud":["other-service"],"sub":"alice"}`,
	} {
		in.Claims = json.RawMessage(claims)
		result, err := p.Execute(ctx, in)
		if err != nil || result.Receipts[0].Outcome != eventstore.Committed {
			t.Fatal("independent claims actor collided", result, err)
		}
	}
}

func TestPrincipalNamespaceTakesPriorityAndExplicitKeyOverrides(t *testing.T) {
	ctx := context.Background()
	p := basic()
	in := req()
	in.Claims = json.RawMessage(`{"iss":"issuer","sub":"first"}`)
	if _, err := p.Execute(ctx, in); err != nil {
		t.Fatal(err)
	}
	in.Claims = json.RawMessage(`{"iss":"issuer","sub":"second"}`)
	if _, err := p.Execute(ctx, in); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal("claims replaced trusted principal namespace", err)
	}
	p = basic()
	p.Main = func(_ context.Context, in Request) (Result, error) {
		return Result{Events: []eventstore.Input{{Key: "explicit-global-key", Action: "set", Payload: in.Payload}}}, nil
	}
	first, err := p.Execute(ctx, req())
	if err != nil {
		t.Fatal(err)
	}
	if first.Receipts[0].Event.Input.Key != "explicit-global-key" {
		t.Fatal("explicit key rewritten")
	}
	other := req()
	other.Context = Context{"principal": json.RawMessage(`"other"`)}
	if _, err = p.Execute(ctx, other); !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal("explicit key was implicitly namespaced", err)
	}
}
