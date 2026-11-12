package eventstore

import (
	"context"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

func (s *Store) Count(ctx context.Context, root eventlib.Root) (int64, error) {
	if err := validateRoot(root); err != nil {
		return 0, eventlib.ErrInvalid
	}
	var count int64
	err := s.Backend.View(ctx, func(r eventlib.Reader) error { var err error; count, err = r.Count(ctx, root); return err })
	return count, err
}

// StreamAggregates holds one repeatable read and invokes the consumer serially.
// Root identities are collected before replay so adapters need not multiplex an
// event cursor over an open roots cursor on the same transaction connection.
func (s *Store) StreamAggregates(ctx context.Context, network, service, domain, id string, yield func(eventlib.Aggregate) error) error {
	scope := eventlib.Root{Network: network, Service: service, Domain: domain, ID: "scope"}
	if validateRoot(scope) != nil || yield == nil {
		return eventlib.ErrInvalid
	}
	return s.Backend.View(ctx, func(r eventlib.Reader) error {
		var roots []eventlib.Root
		if id != "" {
			scope.ID = id
			if validateRoot(scope) != nil {
				return eventlib.ErrInvalid
			}
			roots = []eventlib.Root{scope}
		} else {
			if err := r.Roots(ctx, func(root eventlib.Root) error {
				if root.Network == network && root.Service == service && root.Domain == domain {
					roots = append(roots, root)
				}
				return ctx.Err()
			}); err != nil {
				return err
			}
		}
		for _, root := range roots {
			if err := ctx.Err(); err != nil {
				return err
			}
			aggregate, err := s.aggregate(ctx, r, root)
			if err != nil {
				return err
			}
			if aggregate.Version >= 0 {
				if err = yield(aggregate); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s *Store) StreamRoots(ctx context.Context, network, service, domain string, yield func(eventlib.Root) error) error {
	if validateRoot(eventlib.Root{Network: network, Service: service, Domain: domain, ID: "scope"}) != nil || yield == nil {
		return eventlib.ErrInvalid
	}
	return s.Backend.View(ctx, func(r eventlib.Reader) error {
		return r.Roots(ctx, func(root eventlib.Root) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if root.Network == network && root.Service == service && root.Domain == domain {
				return yield(root)
			}
			return nil
		})
	})
}
