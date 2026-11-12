// Package projection owns current-source play, bounded replay scheduling and
// post-commit notifications. Authentication and transport belong to consumers.
package projection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

var (
	ErrCycle   = errors.New("projection dependency cycle")
	ErrBudget  = errors.New("projection work budget exceeded")
	ErrHandler = errors.New("projection handler not registered")
)

// Consumer-owned seams are implemented by eventstore.Store and view.Store.
type Aggregator interface {
	Aggregate(context.Context, eventlib.Root) (eventlib.Aggregate, error)
}
type Writer interface {
	Batch(context.Context, []view.Mutation) ([]view.Result, error)
}
type Source struct{ Network, Service, Domain string }

func source(root eventlib.Root) Source { return Source{root.Network, root.Service, root.Domain} }

// Trigger is supplied only by a trusted event-delivery adapter. Manual plays
// and replay scheduling omit it, so caller-supplied actions cannot enable push.
type Trigger struct {
	Root    eventlib.Root
	Version int64
	Action  string
}
type Request struct {
	RunID RunID `json:"runId,omitempty"`
	Root  eventlib.Root
	Event *Trigger
	Flag  string
}
type Input struct {
	Aggregate    eventlib.Aggregate
	Action, Flag string
}
type Handler func(context.Context, Input) (Plan, error)
type Dependency struct {
	Root     eventlib.Root
	Flag     string
	Optional bool
}
type Plan struct {
	Mutations    []Mutation
	Dependencies []Dependency
}

// Mutations cannot override source identity/revision: Play binds those from the
// actual aggregate. Patches retain view's mandatory Expected base-version rule.
type Mutation struct {
	ID         string
	Body       json.RawMessage
	Expected   *int64
	Operations []view.Operation
	If         []eventlib.Predicate
	Delete     bool
	Tombstone  bool
}
type Muted struct {
	Source  Source
	Actions []string
}
type Limits struct{ MaxNodes, MaxMutations int }

// Notifications are versioned invalidations, not fabricated post-update bodies.
// A consumer can fetch current scoped view data; creation timestamps/legacy
// traces are not present in the Go storage protocol and are not invented here.
type Notification struct {
	ID      string
	Root    eventlib.Root
	Version int64
	Deleted bool
}
type PushFailure struct {
	Notification Notification
	Err          error
}
type Result struct {
	Rows         []view.Result
	Visited      int
	Ignored      bool
	Committed    bool
	Uncertain    bool
	PushFailures []PushFailure
}
type Engine struct {
	Aggregates Aggregator
	Views      Writer
	Handlers   map[Source]Handler
	Muted      []Muted
	Limits     Limits
	Push       func(context.Context, Notification) error
}

// Play resolves the entire dependency graph before one durable view batch.
// A push failure is recorded separately and never claims that batch rolled back.
func (e *Engine) Play(ctx context.Context, request Request) (Result, error) {
	result := Result{Rows: []view.Result{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.Event != nil {
		copy := *request.Event
		request.Event = &copy
	}
	if e.Aggregates == nil || e.Views == nil {
		return result, fmt.Errorf("projection requires aggregate and view owners")
	}
	if err := validateRequest(request); err != nil {
		return result, err
	}
	limits, err := e.Limits.normalized()
	if err != nil {
		return result, err
	}
	if e.Handlers[source(request.Root)] == nil {
		result.Ignored = true
		return result, nil
	}
	g := graph{engine: e, limits: limits, active: map[eventlib.Root]bool{}, done: map[node]bool{}}
	err = g.visit(ctx, request, false)
	result.Visited = g.visited
	if err != nil {
		return result, err
	}
	if len(g.mutations) == 0 {
		return result, nil
	}
	rows, err := e.Views.Batch(ctx, g.mutations)
	if err != nil {
		result.Uncertain = errors.Is(err, eventlib.ErrUncertain)
		return result, err
	}
	result.Committed = true
	result.Rows = rows
	if err = checkResults(rows, g.mutations); err != nil {
		return result, fmt.Errorf("view batch acknowledged commit: %w", err)
	}
	for i, row := range rows {
		if !row.Applied || !g.notify[i] || e.Push == nil {
			continue
		}
		mutation := g.mutations[i]
		notice := Notification{ID: row.ID, Root: mutation.Root, Version: row.Version, Deleted: mutation.Delete || mutation.Tombstone}
		if err = ctx.Err(); err == nil {
			err = e.Push(ctx, notice)
		}
		if err != nil {
			result.PushFailures = append(result.PushFailures, PushFailure{notice, err})
		}
	}
	return result, nil
}
func (l Limits) normalized() (Limits, error) {
	if l.MaxNodes < 0 || l.MaxMutations < 0 {
		return l, fmt.Errorf("negative projection budget")
	}
	if l.MaxNodes == 0 {
		l.MaxNodes = 64
	}
	if l.MaxMutations == 0 {
		l.MaxMutations = 1000
	}
	return l, nil
}
func validateRoot(root eventlib.Root) error {
	for _, field := range []string{root.Network, root.Service, root.Domain, root.ID} {
		if strings.TrimSpace(field) == "" || !utf8.ValidString(field) {
			return fmt.Errorf("complete projection source root required")
		}
	}
	_, err := canonicaljson.Encode(root)
	return err
}
func validateVersion(version int64) error {
	if version < 0 {
		return fmt.Errorf("negative source revision")
	}
	_, err := canonicaljson.Encode(version)
	return err
}
func validateRequest(r Request) error {
	if err := validateRoot(r.Root); err != nil {
		return err
	}
	if r.Event != nil {
		if r.Event.Root != r.Root || strings.TrimSpace(r.Event.Action) == "" || !utf8.ValidString(r.Event.Action) {
			return fmt.Errorf("event trigger must match source root and have an action")
		}
		return validateVersion(r.Event.Version)
	}
	return nil
}
func (e *Engine) pushes(r Request) bool {
	if r.Event == nil {
		return false
	}
	for _, muted := range e.Muted {
		if muted.Source == source(r.Root) {
			for _, action := range muted.Actions {
				if action == r.Event.Action {
					return false
				}
			}
		}
	}
	return true
}
func checkResults(rows []view.Result, mutations []view.Mutation) error {
	if len(rows) != len(mutations) {
		return fmt.Errorf("view batch returned wrong result count")
	}
	for i, row := range rows {
		if row.ID != mutations[i].ID || (row.Applied && row.Version != mutations[i].Version) {
			return fmt.Errorf("view batch returned wrong identity/revision")
		}
	}
	return nil
}
