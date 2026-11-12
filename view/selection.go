package view

import (
	"context"
	"fmt"

	"blossm.local/backend/eventstore"
)

// MutateWhere selects all matching owned rows and commits their mutations in
// one update transaction. maximum is an explicit 1..MaxLimit selection budget;
// exceeding it aborts before build or writes. Query pagination is not accepted.
// build is trusted, retryable and side-effect-free. It must retain ID/root;
// Expected is bound to the selected version; Version must equal the committed
// source Count-1, proven after root locking. Append the source event first.
// Predicate membership is evaluated
// at the transaction snapshot, not a serializable exclusion of future phantoms.
func (s *Store) MutateWhere(ctx context.Context, q Query, maximum int, build func(context.Context, eventstore.Projection) (Mutation, error)) ([]Result, error) {
	if err := s.Scope.validate(); err != nil {
		return nil, err
	}
	if maximum < 1 || maximum > MaxLimit || q.Offset != 0 || q.Limit != 0 {
		return nil, invalid(fmt.Errorf("selection requires explicit budget and no pagination"))
	}
	if build == nil {
		return nil, fmt.Errorf("selection mutation builder required")
	}
	q.Limit = maximum
	if err := q.validate(); err != nil {
		return nil, invalid(err)
	}
	var result []Result
	err := s.Backend.Update(ctx, func(tx eventstore.Tx) error {
		result = nil
		page := Page{Items: []eventstore.Projection{}}
		if err := s.scan(ctx, tx, q, &page, maximum); err != nil {
			return err
		}
		if len(q.Sort) > 0 {
			if err := sortPage(ctx, &page, q); err != nil {
				return err
			}
		}
		if len(page.Items) == 0 {
			result = []Result{}
			return nil
		}
		mutations := make([]Mutation, 0, len(page.Items))
		for _, p := range page.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			p.Body = append([]byte(nil), p.Body...)
			m, err := build(ctx, p)
			if err != nil {
				return err
			}
			if m.ID != p.ID || m.Root != p.Root {
				return invalid(fmt.Errorf("selected mutation identity changed"))
			}
			expected := p.Version
			if m.Expected != nil && *m.Expected != expected {
				return eventstore.ErrConflict
			}
			m.Expected = &expected
			mutations = append(mutations, m)
		}
		roots, err := s.validateBatch(mutations)
		if err != nil {
			return err
		}
		if err = tx.LockRoots(ctx, roots); err != nil {
			return err
		}
		if err = s.proveSourceVersions(ctx, tx, mutations); err != nil {
			return err
		}
		result, err = s.applyMutations(ctx, tx, mutations)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// A query mutation shares the source projection fence, so it must follow a
// committed source event. Remote source getters are trusted and must return
// committed counts for the exact root; they do not join the local transaction.
func (s *Store) proveSourceVersions(ctx context.Context, tx eventstore.Tx, mutations []Mutation) error {
	countFn := s.SourceCount
	if countFn == nil {
		countFn = tx.Count
	}
	counts := map[eventstore.Root]int64{}
	for _, m := range mutations {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, ok := counts[m.Root]
		if !ok {
			var err error
			count, err = countFn(ctx, m.Root)
			if err != nil {
				return err
			}
			if count < 0 {
				return fmt.Errorf("source returned negative event count")
			}
			counts[m.Root] = count
		}
		if count == 0 || m.Version != count-1 {
			return invalid(fmt.Errorf("query mutation root %.256s version %d must equal committed source frontier %d", m.Root.Key(), m.Version, count-1))
		}
	}
	return ctx.Err()
}
