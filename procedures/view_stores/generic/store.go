package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	querymatch "github.com/mejango/blossm-toolbox-go/lib/query"
)

// Scope is supplied by trusted application composition, never by an untrusted
// query. Owner predicates are ANDed separately and cannot be overridden by it.
// They address body fields; Source domain identity is checked independently.
type Scope struct {
	Network string
	Service string
	Domain  string
	Owner   []eventlib.Predicate
	// OwnerAny is an OR of nonempty conjunctions, ANDed with Owner.
	OwnerAny [][]eventlib.Predicate
	// RequireOwnerAny denies every row when membership lookup returns zero groups.
	RequireOwnerAny bool
}
type Store struct {
	// Name is trusted application configuration, never caller query input.
	Name    string
	Backend eventlib.Backend
	Scope   Scope
	// SourceCount is a trusted remote source getter for MutateWhere only.
	// Nil uses the current update transaction Count. Return event count, not last version.
	SourceCount func(context.Context, eventlib.Root) (int64, error)
}

// ProjectionReader is an optional storage capability. Return nondeleted rows in
// the specified source scope, ascending by ID strictly after afterID, <=limit.
// All pages must share the enclosing Reader's repeatable transaction snapshot.
type ProjectionReader interface {
	Projections(ctx context.Context, network, service, domain, afterID string, limit int) ([]eventlib.Projection, error)
}

type Mutation struct {
	ID      string        `json:"id"`
	Root    eventlib.Root `json:"root"`
	Version int64         `json:"version"`
	// Expected is mandatory for patches, with -1 meaning absent. A patch is not
	// a complete source state and must never silently skip a missing base version.
	Expected   *int64               `json:"expected,omitempty"`
	Body       json.RawMessage      `json:"body,omitempty"`
	Operations []Operation          `json:"operations,omitempty"`
	If         []eventlib.Predicate `json:"if,omitempty"`
	Delete     bool                 `json:"delete,omitempty"`
	// Tombstone persists a versioned deletion even when absent. It requires a
	// full owner-scoped Body and excludes Delete and Operations.
	Tombstone bool `json:"tombstone,omitempty"`
}
type Result struct {
	ID      string `json:"id"`
	Applied bool   `json:"applied"`
	Version int64  `json:"version"`
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.Network) == "" || strings.TrimSpace(s.Service) == "" || strings.TrimSpace(s.Domain) == "" || (len(s.Owner) == 0 && len(s.OwnerAny) == 0 && !s.RequireOwnerAny) {
		return fmt.Errorf("view requires source scope and owner predicates")
	}
	if err := predicatesValid(s.Owner); err != nil {
		return err
	}
	for _, group := range s.OwnerAny {
		if len(group) == 0 {
			return fmt.Errorf("empty owner group")
		}
		if err := predicatesValid(group); err != nil {
			return err
		}
	}
	return nil
}
func (s Scope) contains(root eventlib.Root) bool {
	return root.Network == s.Network && root.Service == s.Service && root.Domain == s.Domain
}
func (s Scope) owns(p eventlib.Projection) (bool, error) {
	if !s.contains(p.Root) {
		return false, nil
	}
	common, err := querymatch.Matches(p.Body, s.Owner)
	if err != nil || !common {
		return common, err
	}
	if len(s.OwnerAny) == 0 {
		return !s.RequireOwnerAny, nil
	}
	for _, group := range s.OwnerAny {
		match, err := querymatch.Matches(p.Body, group)
		if err != nil || match {
			return match, err
		}
	}
	return false, nil
}
func (s *Store) Get(ctx context.Context, id string) (eventlib.Projection, error) {
	var p eventlib.Projection
	if err := s.Scope.validate(); err != nil {
		return p, err
	}
	if !s.validID(id) {
		return p, invalid(fmt.Errorf("view id required"))
	}
	err := s.Backend.View(ctx, func(r eventlib.Reader) error {
		var err error
		p, err = r.Projection(ctx, PhysicalID(s.Name, id))
		if err != nil {
			return err
		}
		owned, err := s.Scope.owns(p)
		if err != nil {
			return err
		}
		if !owned || p.Deleted {
			return eventlib.ErrNotFound
		}
		return nil
	})
	if err != nil {
		return eventlib.Projection{}, err
	}
	p.ID = id
	return p, nil
}

// Batch commits all applied mutations together; any error rolls them all back.
// Stale versions and false If predicates are explicit non-applied results.
// No external side effects belong in a retryable backend callback.
func (s *Store) Batch(ctx context.Context, mutations []Mutation) ([]Result, error) {
	if err := s.Scope.validate(); err != nil {
		return nil, err
	}
	roots, err := s.validateBatch(mutations)
	if err != nil {
		return nil, err
	}

	var result []Result
	err = s.Backend.Update(ctx, func(tx eventlib.Tx) error {
		var err error
		result, err = s.mutateBatch(ctx, tx, mutations, roots)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func (s *Store) validateMutation(m Mutation) error {
	if !s.validID(m.ID) || m.Root.ID == "" || !s.Scope.contains(m.Root) || m.Version < 0 {
		return fmt.Errorf("invalid scoped view identity or version")
	}
	if m.Expected != nil && *m.Expected < -1 {
		return fmt.Errorf("invalid expected version")
	}
	if _, err := canonicaljson.Encode(m.Version); err != nil {
		return err
	}
	if m.Expected != nil {
		if _, err := canonicaljson.Encode(*m.Expected); err != nil {
			return err
		}
	}
	if err := predicatesValid(m.If); err != nil {
		return err
	}
	if m.Tombstone {
		if m.Delete || len(m.Operations) > 0 {
			return fmt.Errorf("tombstone cannot include delete or operations")
		}
		body, err := Apply(m.Body, nil)
		if err != nil {
			return err
		}
		owned, err := s.Scope.owns(eventlib.Projection{Root: m.Root, Body: body})
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("tombstone body is outside owner scope")
		}
		return nil
	}
	if m.Delete {
		if len(m.Body) > 0 || len(m.Operations) > 0 {
			return fmt.Errorf("delete cannot include body or operations")
		}
		return nil
	}
	if len(m.Operations) > 0 {
		if len(m.Body) > 0 || m.Expected == nil {
			return fmt.Errorf("patch requires expected version and no replacement body")
		}
		for _, op := range m.Operations {
			if _, err := op.validate(); err != nil {
				return err
			}
		}
		return nil
	}
	_, err := Apply(m.Body, nil)
	return err
}
func (s *Store) mutate(ctx context.Context, tx eventlib.Tx, m Mutation) (Result, error) {
	result := Result{ID: m.ID, Version: -1}
	current, err := tx.Projection(ctx, PhysicalID(s.Name, m.ID))
	exists := err == nil
	if err != nil && !errors.Is(err, eventlib.ErrNotFound) {
		return result, err
	}
	if exists {
		owned, err := s.Scope.owns(current)
		if err != nil {
			return result, err
		}
		if !owned {
			return result, eventlib.ErrNotFound
		}
		if current.Root != m.Root {
			return result, eventlib.ErrConflict
		}
		result.Version = current.Version
		if m.Version <= current.Version {
			return result, nil
		}
	}
	if m.Expected != nil && *m.Expected != result.Version {
		return result, eventlib.ErrConflict
	}
	if len(m.If) > 0 {
		if !exists || current.Deleted {
			return result, nil
		}
		matches, err := querymatch.Matches(current.Body, m.If)
		if err != nil || !matches {
			return result, err
		}
	}
	if m.Delete && !exists {
		return result, nil
	}
	body := m.Body
	if m.Delete {
		body = current.Body
	} else if len(m.Operations) > 0 {
		if exists && current.Deleted {
			return result, eventlib.ErrConflict
		}
		base := current.Body
		if !exists {
			base = json.RawMessage(`{}`)
		}
		value, parseErr := objectBody(base)
		if parseErr != nil {
			return result, fmt.Errorf("stored view body: %w", parseErr)
		}
		body, err = applyOperations(value, m.Operations)
	} else {
		body, err = Apply(body, nil)
	}
	if err != nil {
		return result, err
	}
	next := eventlib.Projection{ID: PhysicalID(s.Name, m.ID), Root: m.Root, Version: m.Version, Body: body, Deleted: m.Delete || m.Tombstone}
	owned, err := s.Scope.owns(next)
	if err != nil {
		return result, err
	}
	if !owned {
		return result, invalid(fmt.Errorf("mutation leaves owner scope"))
	}
	result.Applied, err = tx.PutProjection(ctx, next)
	if result.Applied {
		result.Version = m.Version
	}
	return result, err
}

func (s *Store) validateBatch(mutations []Mutation) ([]eventlib.Root, error) {
	if len(mutations) == 0 {
		return nil, invalid(fmt.Errorf("empty view batch"))
	}
	roots := make([]eventlib.Root, 0, len(mutations))
	ids := map[string]bool{}
	for _, m := range mutations {
		if err := s.validateMutation(m); err != nil {
			return nil, invalid(err)
		}
		if ids[m.ID] {
			return nil, invalid(fmt.Errorf("duplicate view id in batch; compose its operations instead"))
		}
		ids[m.ID] = true
		roots = append(roots, m.Root)
	}
	return roots, nil
}

func (s *Store) mutateBatch(ctx context.Context, tx eventlib.Tx, mutations []Mutation, roots []eventlib.Root) ([]Result, error) {
	if err := tx.LockRoots(ctx, roots); err != nil {
		return nil, err
	}
	return s.applyMutations(ctx, tx, mutations)
}
func (s *Store) applyMutations(ctx context.Context, tx eventlib.Tx, mutations []Mutation) ([]Result, error) {
	result := make([]Result, 0, len(mutations))
	for _, m := range mutations {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r, err := s.mutate(ctx, tx, m)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}
