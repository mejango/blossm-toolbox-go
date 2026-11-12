package eventstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

// HistoryOptions selects event-number or inclusive created-time replay. Historical
// replay deliberately ignores current snapshots: they may contain later events.
// Results are historical facts and must not be used as current projection writes.
type HistoryOptions struct {
	ThroughNumber  *int64
	ThroughCreated *time.Time
	IncludeEvents  bool
}
type HistoricalAggregate struct {
	Root               eventlib.Root    `json:"root"`
	LastIncludedNumber int64            `json:"lastIncludedNumber"`
	State              json.RawMessage  `json:"state"`
	Context            json.RawMessage  `json:"context"`
	Events             []eventlib.Event `json:"events,omitempty"`
}

func (s *Store) History(ctx context.Context, root eventlib.Root, options HistoryOptions) (HistoricalAggregate, error) {
	result := HistoricalAggregate{Root: root, LastIncludedNumber: -1, State: json.RawMessage(`{}`), Context: json.RawMessage(`{}`)}
	if options.ThroughNumber != nil && *options.ThroughNumber < 0 {
		return result, eventlib.ErrInvalid
	}
	if validateRoot(root) != nil {
		return result, eventlib.ErrInvalid
	}
	reducer, err := s.reducer(root)
	if err != nil {
		return result, err
	}
	err = s.Backend.View(ctx, func(r eventlib.Reader) error {
		result.State = json.RawMessage(`{}`)
		result.Context = json.RawMessage(`{}`)
		result.Events = nil
		result.LastIncludedNumber = -1
		last := int64(-1)
		merged := map[string]json.RawMessage{}
		return r.Events(ctx, root, -1, func(event eventlib.Event) error {
			if event.Number != last+1 {
				return fmt.Errorf("historical event sequence gap")
			}
			last = event.Number
			if err := Verify(event); err != nil {
				return err
			}
			if options.ThroughNumber != nil && event.Number > *options.ThroughNumber {
				return nil
			}
			if options.ThroughCreated != nil {
				created, err := time.Parse(time.RFC3339Nano, event.Input.Created)
				if err != nil {
					return err
				}
				if created.After(*options.ThroughCreated) {
					return nil
				}
			}
			next, err := applyReducer(reducer, result.State, event)
			if err != nil {
				return err
			}
			result.State, err = canonicaljson.Canonical(next)
			if err != nil {
				return err
			}
			var values map[string]json.RawMessage
			if err = json.Unmarshal(event.Input.Context, &values); err != nil {
				return err
			}
			for k, v := range values {
				merged[k] = v
			}
			result.Context, err = canonicaljson.Encode(merged)
			if err != nil {
				return err
			}
			result.LastIncludedNumber = event.Number
			if options.IncludeEvents {
				result.Events = append(result.Events, event)
			}
			return nil
		})
	})
	return result, err
}
