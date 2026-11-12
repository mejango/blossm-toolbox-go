package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mejango/blossm-toolbox-go/eventstore"
)

const MaxReplayBatch = 10000

// RunID identifies one logical replay. Reuse it for retries and pages, and choose
// a fresh ID for each new run. It is never generated per message or process.
type RunID string

func (id RunID) Validate() error {
	if strings.TrimSpace(string(id)) == "" || len(id) > 1024 {
		return fmt.Errorf("%w: replay run ID must be nonblank and at most 1024 bytes", eventstore.ErrInvalid)
	}
	return nil
}

// RootStream must await callbacks serially, stop on their error and never
// replay them. Roots must be strictly ascending by Root.Key across all scopes.
type RootStream interface {
	StreamRoots(context.Context, func(eventstore.Root) error) error
}
type Producer interface {
	Enqueue(context.Context, Request) error
}
type BackendRoots struct{ Backend eventstore.Backend }

func (r BackendRoots) StreamRoots(ctx context.Context, fn func(eventstore.Root) error) error {
	if r.Backend == nil {
		return fmt.Errorf("replay backend required")
	}
	return r.Backend.View(ctx, func(reader eventstore.Reader) error {
		return reader.Roots(ctx, func(root eventstore.Root) error {
			count, err := reader.Count(ctx, root)
			if err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
			return fn(root)
		})
	})
}

type Scheduler struct {
	Roots    RootStream
	Producer Producer
}
type ReplayResult struct {
	RunID    RunID
	Enqueued int
	// After advances only after confirmed enqueue. A failed/uncertain enqueue may
	// be delivered again on resume; scheduling does not promise exactly once.
	After    string
	Complete bool
}

var pageFull = errors.New("replay page collected")

// Replay prepares at most limit+1 scoped identities before any external enqueue.
// Producer failures return confirmed partial progress, never a rollback claim.
func (s Scheduler) Replay(ctx context.Context, source Source, runID RunID, after string, limit int) (ReplayResult, error) {
	result := ReplayResult{RunID: runID, After: after}
	if err := runID.Validate(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.Roots == nil || s.Producer == nil {
		return result, fmt.Errorf("replay root source and producer required")
	}
	if limit < 1 || limit > MaxReplayBatch {
		return result, fmt.Errorf("replay limit must be 1..%d", MaxReplayBatch)
	}
	if err := validateRoot(eventstore.Root{Network: source.Network, Service: source.Service, Domain: source.Domain, ID: "scope"}); err != nil {
		return result, err
	}
	if after != "" {
		var root eventstore.Root
		if err := eventstore.Decode(json.RawMessage(after), &root); err != nil {
			return result, fmt.Errorf("invalid replay cursor: %w", err)
		}
		if err := validateRoot(root); err != nil {
			return result, err
		}
		if (Source{root.Network, root.Service, root.Domain}) != source {
			return result, fmt.Errorf("replay cursor source mismatch")
		}
		after = root.Key()
		result.After = after
	}
	roots := make([]eventstore.Root, 0, limit+1)
	previous := ""
	err := s.Roots.StreamRoots(ctx, func(root eventstore.Root) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateRoot(root); err != nil {
			return err
		}
		key := root.Key()
		if key <= previous {
			return fmt.Errorf("replay root order did not advance")
		}
		previous = key
		if (Source{root.Network, root.Service, root.Domain}) != source || key <= after {
			return nil
		}
		roots = append(roots, root)
		if len(roots) > limit {
			return pageFull
		}
		return nil
	})
	if err != nil && !errors.Is(err, pageFull) {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	more := len(roots) > limit
	if more {
		roots = roots[:limit]
	}
	for _, root := range roots {
		if err = ctx.Err(); err != nil {
			return result, err
		}
		if err = s.Producer.Enqueue(ctx, Request{RunID: runID, Root: root}); err != nil {
			return result, err
		}
		result.Enqueued++
		result.After = root.Key()
	}
	result.Complete = !more
	return result, nil
}
