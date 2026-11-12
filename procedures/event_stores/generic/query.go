package eventstore

import (
	"context"
	"encoding/json"
	"fmt"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	querymatch "github.com/mejango/blossm-toolbox-go/lib/query"
)

// Query filters complete current state after indexed candidate discovery.
func (s *Store) Query(ctx context.Context, network, service, domain string, predicates []eventlib.Predicate) ([]eventlib.Aggregate, error) {
	if _, err := querymatch.Matches(json.RawMessage(`{}`), predicates); err != nil {
		return nil, err
	}
	result := []eventlib.Aggregate{}
	err := s.Backend.View(ctx, func(r eventlib.Reader) error {
		result = []eventlib.Aggregate{}
		var roots []eventlib.Root
		if candidates, ok := r.(eventlib.CandidateReader); ok {
			var err error
			roots, err = candidates.Candidates(ctx, network, service, domain, predicates)
			if err != nil {
				return err
			}
		} else {
			if e := r.Roots(ctx, func(root eventlib.Root) error {
				if root.Network == network && root.Service == service && root.Domain == domain {
					roots = append(roots, root)
				}
				return nil
			}); e != nil {
				return e
			}
		}
		for _, root := range roots {
			a, e := s.aggregate(ctx, r, root)
			if e != nil {
				return e
			}
			ok, e := querymatch.Matches(a.State, predicates)
			if e != nil {
				return e
			}
			if ok && a.Version >= 0 {
				result = append(result, a)
			}
		}
		return nil
	})
	return result, err
}
func (s *Store) Project(ctx context.Context, p eventlib.Projection) (bool, error) {
	if p.ID == "" || p.Version < 0 {
		return false, fmt.Errorf("invalid projection identity")
	}
	if err := validateRoot(p.Root); err != nil {
		return false, err
	}
	b, e := canonicaljson.Canonical(p.Body)
	if e != nil {
		return false, e
	}
	p.Body = b
	var applied bool
	err := s.Backend.Update(ctx, func(tx eventlib.Tx) error { var e error; applied, e = tx.PutProjection(ctx, p); return e })
	return applied, err
}
