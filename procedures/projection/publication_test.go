package projection

import (
	"context"
	"errors"
	"testing"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

type publicationBackend struct {
	eventlib.Backend
	event eventlib.Event
	err   error
}
type publicationReader struct {
	eventlib.Reader
	event eventlib.Event
	err   error
}

func (b publicationBackend) View(ctx context.Context, fn func(eventlib.Reader) error) error {
	return fn(publicationReader{event: b.event, err: b.err})
}
func (r publicationReader) Find(_ context.Context, key string) (eventlib.Event, error) {
	if key != r.event.Input.Key {
		return eventlib.Event{}, eventlib.ErrNotFound
	}
	return r.event, r.err
}
func storedPublication(t *testing.T) eventlib.Publication {
	t.Helper()
	in := eventlib.Input{Root: testRoot("bird"), Action: "sang", Key: "event", Payload: raw(`{"sound":"chirp"}`)}
	f, err := canonicaljson.Digest(in)
	if err != nil {
		t.Fatal(err)
	}
	e := eventlib.Event{Input: in, Number: 2, Fingerprint: f}
	e.Hash, err = canonicaljson.Digest(e)
	if err != nil {
		t.Fatal(err)
	}
	return eventlib.Publication{ID: e.Hash, Event: e}
}
func TestPublicationVerifiedTriggerAndBestEffortPush(t *testing.T) {
	p := storedPublication(t)
	e := engine()
	committed := false
	observed := 0
	pushes := 0
	e.Handlers[source(p.Event.Input.Root)] = func(_ context.Context, in Input) (Plan, error) {
		if in.Action != "sang" {
			t.Fatal(in.Action)
		}
		return simplePlan(context.Background(), in)
	}
	e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
		committed = true
		return applied(m), nil
	})
	e.Push = func(context.Context, Notification) error {
		pushes++
		if !committed {
			t.Fatal("push before commit")
		}
		return errors.New("offline")
	}
	adapter := PublicationAdapter{Backend: publicationBackend{event: p.Event}, Engine: &e, OnPushFailure: func(_ context.Context, f PushFailure) {
		observed++
		if f.Err == nil {
			t.Fatal(f)
		}
	}}
	result, err := adapter.Consume(context.Background(), p)
	if err != nil || !result.Committed || len(result.PushFailures) != 1 || observed != 1 || pushes != 1 {
		t.Fatal(result, err, observed, pushes)
	}
	// A duplicate/stale view write is acknowledged and must not push again.
	e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
		r := applied(m)
		for i := range r {
			r[i].Applied = false
		}
		return r, nil
	})
	if err = adapter.Publish(context.Background(), p); err != nil || pushes != 1 {
		t.Fatal(err, pushes)
	}
}
func TestPublicationRejectsForgedOrMissingSource(t *testing.T) {
	for _, mode := range []string{"tamper", "id", "missing", "different-stored", "uncertain", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			p := storedPublication(t)
			stored := p.Event
			e := engine()
			calls := 0
			e.Views = writerFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
				calls++
				return nil, eventlib.ErrUncertain
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var readErr error
			switch mode {
			case "tamper":
				p.Event.Input.Action = "forged"
			case "id":
				p.ID = "wrong"
			case "missing":
				readErr = eventlib.ErrNotFound
			case "different-stored":
				stored.Input.Action = "other"
				stored.Fingerprint, _ = canonicaljson.Digest(stored.Input)
				stored.Hash = ""
				stored.Hash, _ = canonicaljson.Digest(stored)
			case "canceled":
				cancel()
			}
			adapter := PublicationAdapter{Backend: publicationBackend{event: stored, err: readErr}, Engine: &e}
			_, err := adapter.Consume(ctx, p)
			if err == nil {
				t.Fatal("accepted", mode)
			}
			if mode == "uncertain" {
				if !errors.Is(err, eventlib.ErrUncertain) || calls != 1 {
					t.Fatal(err, calls)
				}
			} else if calls != 0 {
				t.Fatal("unverified effect", calls)
			}
		})
	}
}
