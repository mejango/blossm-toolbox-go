package projection

import (
	"context"
	"fmt"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/event_stores/generic"
)

// PublicationAdapter binds a verified, persisted source event to Play. Backend
// must read the source event store, not the projection destination. Authentication
// and source routing remain the delivery consumer's responsibility.
type PublicationAdapter struct {
	Backend eventlib.Backend
	Engine  *Engine
	// OnPushFailure observes best-effort failures after durable view success.
	// It must return promptly; it cannot roll back the committed view.
	OnPushFailure func(context.Context, PushFailure)
}

func (p PublicationAdapter) Consume(ctx context.Context, notice eventlib.Publication) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if p.Backend == nil || p.Engine == nil {
		return Result{}, fmt.Errorf("publication requires source backend and projection engine")
	}
	if notice.ID == "" || notice.ID != notice.Event.Hash || notice.Attempts < 0 {
		return Result{}, fmt.Errorf("invalid publication identity")
	}
	if err := eventstore.ValidatePublicationSize(notice); err != nil {
		return Result{}, err
	}
	if err := eventstore.Verify(notice.Event); err != nil {
		return Result{}, err
	}
	var persisted eventlib.Event
	err := p.Backend.View(ctx, func(r eventlib.Reader) error {
		var err error
		persisted, err = r.Find(ctx, notice.Event.Input.Key)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	if err = eventstore.Verify(persisted); err != nil {
		return Result{}, err
	}
	if persisted.Hash != notice.Event.Hash {
		return Result{}, fmt.Errorf("publication differs from persisted source event")
	}
	trigger := &Trigger{Root: persisted.Input.Root, Version: persisted.Number, Action: persisted.Input.Action}
	result, err := p.Engine.Play(ctx, Request{Root: trigger.Root, Event: trigger})
	if err == nil && p.OnPushFailure != nil {
		for _, failure := range result.PushFailures {
			p.OnPushFailure(ctx, failure)
		}
	}
	return result, err
}

// Publish acknowledges durable projection work even when best-effort push fails.
func (p PublicationAdapter) Publish(ctx context.Context, notice eventlib.Publication) error {
	_, err := p.Consume(ctx, notice)
	return err
}

var _ eventlib.Publisher = PublicationAdapter{}
