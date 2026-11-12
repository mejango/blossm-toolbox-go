package eventstore

import "context"

func (s *Store) Count(ctx context.Context, root Root) (int64, error) {
	if err := validateRoot(root); err != nil {
		return 0, ErrInvalid
	}
	var count int64
	err := s.Backend.View(ctx, func(r Reader) error { var err error; count, err = r.Count(ctx, root); return err })
	return count, err
}

// StreamAggregates holds one repeatable read and invokes the consumer serially.
// Root identities are collected before replay so adapters need not multiplex an
// event cursor over an open roots cursor on the same transaction connection.
func (s *Store) StreamAggregates(ctx context.Context, network, service, domain, id string, yield func(Aggregate) error) error {
	scope := Root{Network: network, Service: service, Domain: domain, ID: "scope"}
	if validateRoot(scope) != nil || yield == nil {
		return ErrInvalid
	}
	return s.Backend.View(ctx, func(r Reader) error {
		var roots []Root
		if id != "" {
			scope.ID = id
			if validateRoot(scope) != nil {
				return ErrInvalid
			}
			roots = []Root{scope}
		} else {
			if err := r.Roots(ctx, func(root Root) error {
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

func (s *Store) StreamRoots(ctx context.Context, network, service, domain string, yield func(Root) error) error {
	if validateRoot(Root{Network: network, Service: service, Domain: domain, ID: "scope"}) != nil || yield == nil {
		return ErrInvalid
	}
	return s.Backend.View(ctx, func(r Reader) error {
		return r.Roots(ctx, func(root Root) error {
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
