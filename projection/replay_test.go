package projection

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"blossm.local/backend/eventstore"
)

type rootsFn func(context.Context, func(eventstore.Root) error) error

func (f rootsFn) StreamRoots(ctx context.Context, fn func(eventstore.Root) error) error {
	return f(ctx, fn)
}

type producerFn func(context.Context, Request) error

func (f producerFn) Enqueue(ctx context.Context, r Request) error { return f(ctx, r) }
func stream(roots []eventstore.Root, visited *int) RootStream {
	return rootsFn(func(ctx context.Context, fn func(eventstore.Root) error) error {
		for _, root := range roots {
			*visited++
			if err := fn(root); err != nil {
				return err
			}
		}
		return nil
	})
}
func TestReplayBoundedManualSchedulingAndResume(t *testing.T) {
	roots := []eventstore.Root{}
	for i := 0; i < 150; i++ {
		roots = append(roots, testRoot(fmt.Sprintf("%03d", i)))
	}
	visited := 0
	queued := []Request{}
	s := Scheduler{Roots: stream(roots, &visited), Producer: producerFn(func(_ context.Context, r Request) error {
		if r.Event != nil || r.RunID != "run-1" {
			t.Fatal("replay enabled push")
		}
		queued = append(queued, r)
		return nil
	})}
	first, err := s.Replay(context.Background(), source(roots[0]), "run-1", "", 125)
	if err != nil || first.RunID != "run-1" || first.Enqueued != 125 || first.Complete || visited != 126 || first.After != roots[124].Key() {
		t.Fatal(first, err, visited)
	}
	last, err := s.Replay(context.Background(), source(roots[0]), "run-1", first.After, 125)
	if err != nil || last.RunID != first.RunID || last.Enqueued != 25 || !last.Complete || len(queued) != 150 {
		t.Fatal(last, err, len(queued))
	}
	for i, r := range queued {
		if r.Root != roots[i] {
			t.Fatal("replay lost/duplicated root", i, r)
		}
	}
}
func TestReplayConfirmedProgressOnProducerFailure(t *testing.T) {
	roots := []eventstore.Root{testRoot("a"), testRoot("b"), testRoot("c")}
	visited, calls := 0, 0
	failure := errors.New("enqueue acknowledgement lost")
	s := Scheduler{Roots: stream(roots, &visited), Producer: producerFn(func(context.Context, Request) error {
		calls++
		if calls == 2 {
			return failure
		}
		return nil
	})}
	result, err := s.Replay(context.Background(), source(roots[0]), "run-1", "", 3)
	if !errors.Is(err, failure) || result.RunID != "run-1" || result.Enqueued != 1 || result.After != roots[0].Key() || result.Complete || calls != 2 {
		t.Fatal(result, err, calls)
	}
	resumed := []string{}
	s.Producer = producerFn(func(_ context.Context, r Request) error { resumed = append(resumed, r.Root.ID); return nil })
	result, err = s.Replay(context.Background(), source(roots[0]), "run-1", result.After, 3)
	if err != nil || result.Enqueued != 2 || !result.Complete || len(resumed) != 2 || resumed[0] != "b" {
		t.Fatal(result, err, resumed)
	}
}
func TestReplayReadFailureAndInvalidInputsNeverEnqueue(t *testing.T) {
	failure := errors.New("stream failed")
	s := Scheduler{Producer: producerFn(func(context.Context, Request) error { t.Fatal("enqueue despite failed collection"); return nil }), Roots: rootsFn(func(_ context.Context, fn func(eventstore.Root) error) error {
		if err := fn(testRoot("a")); err != nil {
			return err
		}
		return failure
	})}
	if _, err := s.Replay(context.Background(), source(testRoot("a")), "run-1", "", 10); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	for _, limit := range []int{0, -1, MaxReplayBatch + 1} {
		if _, err := s.Replay(context.Background(), source(testRoot("a")), "run-1", "", limit); err == nil {
			t.Fatal("invalid limit")
		}
	}
	for _, cursor := range []string{"invalid", `{}`, `{"network":"other","service":"animals","domain":"bird","id":"a"}`} {
		if _, err := s.Replay(context.Background(), source(testRoot("a")), "run-1", cursor, 10); err == nil {
			t.Fatal("invalid cursor")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Replay(ctx, source(testRoot("a")), "run-1", "", 10); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	visited := 0
	s.Roots = stream([]eventstore.Root{testRoot("b"), testRoot("a")}, &visited)
	if _, err := s.Replay(context.Background(), source(testRoot("a")), "run-1", "", 10); err == nil {
		t.Fatal("unordered root source accepted")
	}
}
func TestReplayScopeIsolation(t *testing.T) {
	roots := []eventstore.Root{testRoot("a"), testRoot("b")}
	other := testRoot("foreign")
	other.Network = "other"
	roots = append(roots, other)
	sort.Slice(roots, func(i, j int) bool { return roots[i].Key() < roots[j].Key() })
	visited, calls := 0, 0
	s := Scheduler{Roots: stream(roots, &visited), Producer: producerFn(func(_ context.Context, r Request) error {
		calls++
		if source(r.Root) != source(testRoot("a")) {
			t.Fatal(r)
		}
		return nil
	})}
	result, err := s.Replay(context.Background(), source(testRoot("a")), "run-1", "", 10)
	if err != nil || result.Enqueued != 2 || calls != 2 || !result.Complete {
		t.Fatal(result, err, calls)
	}
}

func TestReplayRunIdentityAcrossRetriesAndNewRuns(t *testing.T) {
	visited := 0
	var requests []Request
	scheduler := Scheduler{Roots: stream([]eventstore.Root{testRoot("a"), testRoot("b")}, &visited), Producer: producerFn(func(_ context.Context, r Request) error { requests = append(requests, r); return nil })}
	for _, run := range []RunID{"run-1", "run-1", "run-2"} {
		result, err := scheduler.Replay(context.Background(), source(testRoot("a")), run, "", 1)
		if err != nil || result.RunID != run || result.Enqueued != 1 || result.Complete {
			t.Fatal(result, err)
		}
	}
	if requests[0] != requests[1] || requests[0].RunID == requests[2].RunID || requests[0].Root != requests[2].Root {
		t.Fatal(requests)
	}
	before := visited
	for _, run := range []RunID{"", " \t", RunID(string(make([]byte, 1025)))} {
		if _, err := scheduler.Replay(context.Background(), source(testRoot("a")), run, "", 1); !errors.Is(err, eventstore.ErrInvalid) {
			t.Fatal(err)
		}
	}
	if visited != before || len(requests) != 3 {
		t.Fatal("invalid run accessed source/producer")
	}
}
