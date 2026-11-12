package eventstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Predicate struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

func matchPath(v any, path []string, want any) bool {
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if matchPath(x, path, want) {
				return true
			}
		}
		return false
	}
	if len(path) == 0 {
		a, _ := json.Marshal(v)
		b, _ := json.Marshal(want)
		return bytes.Equal(a, b)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	next, ok := m[path[0]]
	return ok && matchPath(next, path[1:], want)
}

// Matches is conjunction of strict scalar equalities. Explicit null differs
// from absence; a path through an array matches any element.
func Matches(state json.RawMessage, predicates []Predicate) (bool, error) {
	d := json.NewDecoder(bytes.NewReader(state))
	d.UseNumber()
	var obj any
	if e := d.Decode(&obj); e != nil {
		return false, e
	}
	matched := true
	for _, p := range predicates {
		for _, part := range strings.Split(p.Path, ".") {
			if part == "" {
				return false, fmt.Errorf("empty query path segment")
			}
		}
		if p.Path == "" {
			return false, fmt.Errorf("empty query path")
		}
		raw, e := Canonical(p.Value)
		if e != nil {
			return false, e
		}
		d = json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		var want any
		if e = d.Decode(&want); e != nil {
			return false, e
		}
		switch want.(type) {
		case map[string]any, []any:
			return false, fmt.Errorf("query value must be scalar")
		}
		if !matchPath(obj, strings.Split(p.Path, "."), want) {
			matched = false
		}
	}
	return matched, nil
}

// CandidateReader narrows clean snapshots while retaining every dirty root.
// Implementations may return false positives, never omit a possible match.
type CandidateReader interface {
	Candidates(context.Context, string, string, string, []Predicate) ([]Root, error)
}

// Query filters complete current state after indexed candidate discovery.
func (s *Store) Query(ctx context.Context, network, service, domain string, predicates []Predicate) ([]Aggregate, error) {
	if _, err := Matches(json.RawMessage(`{}`), predicates); err != nil {
		return nil, err
	}
	result := []Aggregate{}
	err := s.Backend.View(ctx, func(r Reader) error {
		result = []Aggregate{}
		var roots []Root
		if candidates, ok := r.(CandidateReader); ok {
			var err error
			roots, err = candidates.Candidates(ctx, network, service, domain, predicates)
			if err != nil {
				return err
			}
		} else {
			if e := r.Roots(ctx, func(root Root) error {
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
			ok, e := Matches(a.State, predicates)
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
func (s *Store) Project(ctx context.Context, p Projection) (bool, error) {
	if p.ID == "" || p.Version < 0 {
		return false, fmt.Errorf("invalid projection identity")
	}
	if err := validateRoot(p.Root); err != nil {
		return false, err
	}
	b, e := Canonical(p.Body)
	if e != nil {
		return false, e
	}
	p.Body = b
	var applied bool
	err := s.Backend.Update(ctx, func(tx Tx) error { var e error; applied, e = tx.PutProjection(ctx, p); return e })
	return applied, err
}
