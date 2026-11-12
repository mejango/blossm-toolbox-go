package view

import (
	"context"
	"fmt"
	"strings"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

// ComponentReader is a trusted read adapter. It must apply scope to both rows
// and pagination counts. Composite handlers receive only bound Components,
// never this interface or permission to choose a replacement scope.
type ComponentReader interface {
	Get(context.Context, Scope, string) (eventstore.Projection, error)
	List(context.Context, Scope, Query) (Page, error)
}

// BackendComponent adapts existing view reads without duplicating their rules.
type BackendComponent struct {
	Backend eventstore.Backend
	Name    string
}

func (r BackendComponent) Get(ctx context.Context, scope Scope, id string) (eventstore.Projection, error) {
	return (&Store{Backend: r.Backend, Scope: scope, Name: r.Name}).Get(ctx, id)
}
func (r BackendComponent) List(ctx context.Context, scope Scope, q Query) (Page, error) {
	return (&Store{Backend: r.Backend, Scope: scope, Name: r.Name}).List(ctx, q)
}

// Components binds named readers to one authenticated scope and execution
// context. Get/List offer no override for either. Reads may use separate backend
// snapshots: this is composition, not an atomic cross-component read protocol.
type Components struct {
	ctx     context.Context
	scope   Scope
	readers map[string]ComponentReader
}

func (c *Components) reader(name string) (ComponentReader, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	r, ok := c.readers[name]
	if !ok {
		return nil, fmt.Errorf("unknown view component %q", name)
	}
	return r, nil
}
func (c *Components) Get(name, id string) (eventstore.Projection, error) {
	r, err := c.reader(name)
	if err != nil {
		return eventstore.Projection{}, err
	}
	p, err := r.Get(c.ctx, copyScope(c.scope), id)
	if err == nil {
		err = c.ctx.Err()
	}
	if err == nil {
		err = c.check(p)
	}
	if err != nil {
		return eventstore.Projection{}, fmt.Errorf("component %q: %w", name, err)
	}
	return p, nil
}
func (c *Components) List(name string, q Query) (Page, error) {
	r, err := c.reader(name)
	if err != nil {
		return Page{}, err
	}
	page, err := r.List(c.ctx, copyScope(c.scope), q)
	if err == nil {
		err = c.ctx.Err()
	}
	if err == nil {
		for _, p := range page.Items {
			if err = c.check(p); err != nil {
				break
			}
		}
	}
	if err != nil {
		return Page{}, fmt.Errorf("component %q: %w", name, err)
	}
	return page, nil
}
func (c *Components) check(p eventstore.Projection) error {
	owned, err := c.scope.owns(p)
	if err != nil {
		return err
	}
	if !owned || p.Deleted {
		return fmt.Errorf("component returned a view outside caller scope")
	}
	return nil
}

// CompositeMain receives the caller query unchanged, a detached copy of scope,
// and named read capabilities bound to the original scope. Scope is supplied by
// authenticated application composition, not parsed from query fields.
type CompositeMain[Q, R any] func(context.Context, Q, Scope, *Components) (R, error)

// ExecuteComposite delegates to main and returns its typed response unchanged.
// Errors return the zero response. Cancellation is cooperative: main and reader
// implementations must observe their context. Readers expire when main returns.
func ExecuteComposite[Q, R any](ctx context.Context, scope Scope, query Q, readers map[string]ComponentReader, main CompositeMain[Q, R]) (R, error) {
	var zero R
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := scope.validate(); err != nil {
		return zero, err
	}
	if main == nil {
		return zero, fmt.Errorf("composite main is required")
	}
	bound := make(map[string]ComponentReader, len(readers))
	for name, r := range readers {
		if strings.TrimSpace(name) == "" || r == nil {
			return zero, fmt.Errorf("invalid view component %q", name)
		}
		bound[name] = r
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	components := &Components{ctx: ctx, scope: copyScope(scope), readers: bound}
	result, err := main(ctx, query, copyScope(scope), components)
	if err != nil {
		return zero, err
	}
	if err = ctx.Err(); err != nil {
		return zero, err
	}
	return result, nil
}
func copyScope(scope Scope) Scope {
	scope.Owner = clonePredicates(scope.Owner)
	scope.OwnerAny = append([][]eventstore.Predicate(nil), scope.OwnerAny...)
	for i := range scope.OwnerAny {
		scope.OwnerAny[i] = clonePredicates(scope.OwnerAny[i])
	}
	return scope
}
func clonePredicates(p []eventstore.Predicate) []eventstore.Predicate {
	scope := Scope{Owner: append([]eventstore.Predicate(nil), p...)}
	for i := range scope.Owner {
		scope.Owner[i].Value = append([]byte(nil), scope.Owner[i].Value...)
	}
	return scope.Owner
}
