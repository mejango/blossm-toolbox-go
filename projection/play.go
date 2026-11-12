package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"blossm.local/backend/eventstore"
	"blossm.local/backend/view"
)

type node struct {
	Root eventstore.Root
	Flag string
}
type graph struct {
	engine  *Engine
	limits  Limits
	visited int
	active  map[eventstore.Root]bool
	// done records both successful and optional-missing nodes. A later required
	// reference to a missing node must still fail rather than silently disappear.
	done      map[node]bool
	mutations []view.Mutation
	notify    []bool
}

func (g *graph) visit(ctx context.Context, r Request, optional bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateRequest(r); err != nil {
		return err
	}
	if g.visited >= g.limits.MaxNodes {
		return ErrBudget
	}
	g.visited++
	if g.active[r.Root] {
		return fmt.Errorf("%w: %s", ErrCycle, r.Root.Key())
	}
	key := node{r.Root, r.Flag}
	if exists, seen := g.done[key]; seen {
		if !exists && !optional {
			return eventstore.ErrNotFound
		}
		return nil
	}
	handler := g.engine.Handlers[source(r.Root)]
	if handler == nil {
		return fmt.Errorf("%w: %v", ErrHandler, source(r.Root))
	}
	aggregate, err := g.engine.Aggregates.Aggregate(ctx, r.Root)
	if errors.Is(err, eventstore.ErrNotFound) && optional {
		g.done[key] = false
		return nil
	}
	if err != nil {
		return err
	}
	if aggregate.Root != r.Root {
		return fmt.Errorf("aggregate source identity mismatch")
	}
	if aggregate.Version == -1 {
		if optional {
			g.done[key] = false
			return nil
		}
		return eventstore.ErrNotFound
	}
	if err = validateVersion(aggregate.Version); err != nil {
		return err
	}
	if r.Event != nil && aggregate.Version < r.Event.Version {
		return fmt.Errorf("%w: aggregate predates triggering revision", eventstore.ErrConflict)
	}
	aggregate.State, err = eventstore.Canonical(aggregate.State)
	if err != nil {
		return err
	}
	g.active[r.Root] = true
	defer delete(g.active, r.Root)
	input := Input{Aggregate: aggregate, Flag: r.Flag}
	if r.Event != nil {
		input.Action = r.Event.Action
	}
	plan, err := handler(ctx, input)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Count output before recursing to bound work even for handlers with huge plans.
	if len(plan.Mutations) > g.limits.MaxMutations-len(g.mutations) {
		return ErrBudget
	}
	for _, dependency := range plan.Dependencies {
		if err = g.visit(ctx, Request{Root: dependency.Root, Flag: dependency.Flag}, dependency.Optional); err != nil {
			return err
		}
	}
	if len(plan.Mutations) > g.limits.MaxMutations-len(g.mutations) {
		return ErrBudget
	}
	for _, m := range plan.Mutations {
		mutation := view.Mutation{ID: m.ID, Root: aggregate.Root, Version: aggregate.Version, Body: m.Body, Expected: m.Expected, Operations: m.Operations, If: m.If, Delete: m.Delete, Tombstone: m.Tombstone}
		// Isolate handler-owned slices/maps before passing the prepared batch onward.
		raw, err := json.Marshal(mutation)
		if err != nil {
			return err
		}
		var isolated view.Mutation
		if err = json.Unmarshal(raw, &isolated); err != nil {
			return err
		}
		g.mutations = append(g.mutations, isolated)
		g.notify = append(g.notify, g.engine.pushes(r))
	}
	g.done[key] = true
	return nil
}
