package view

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

const DefaultLimit = 50
const MaxLimit = 10000

// Query predicates address body fields. Results have stable ID order; Offset
// and Total count only matching owned rows, never other owners or tombstones.
type Query struct {
	Predicates []eventstore.Predicate
	Ranges     []Range
	Text       *TextQuery
	Sort       []Sort
	Offset     int
	Limit      int
}
type Page struct {
	Items      []eventstore.Projection `json:"items"`
	Total      int                     `json:"total"`
	NextOffset *int                    `json:"nextOffset,omitempty"`
	Next       string                  `json:"next,omitempty"`
	Updates    string                  `json:"updates,omitempty"`
	Empty      json.RawMessage         `json:"empty,omitempty"`
}

func (s *Store) List(ctx context.Context, q Query) (Page, error) {
	page := Page{Items: []eventstore.Projection{}}
	if err := s.Scope.validate(); err != nil {
		return Page{}, err
	}
	if q.Limit == 0 {
		q.Limit = DefaultLimit
	}
	if err := q.validate(); err != nil {
		return Page{}, invalid(err)
	}

	err := s.Backend.View(ctx, func(r eventstore.Reader) error {
		page = Page{Items: []eventstore.Projection{}}
		return s.scan(ctx, r, q, &page, 0)
	})
	if err != nil {
		return Page{}, err
	}
	if len(q.Sort) > 0 {
		if err := sortPage(ctx, &page, q); err != nil {
			return Page{}, err
		}
	}
	if page.Total > q.Offset && page.Total-q.Offset > len(page.Items) {
		next := q.Offset + len(page.Items)
		page.NextOffset = &next
	}
	return page, nil
}
func (s *Store) collect(p eventstore.Projection, q Query, page *Page) error {
	if p.Deleted {
		return nil
	}
	owned, err := s.Scope.owns(p)
	if err != nil || !owned {
		return err
	}
	matches, err := eventstore.Matches(p.Body, q.Predicates)
	if err != nil || !matches {
		return err
	}
	extra, err := q.matchesExtra(p)
	if err != nil || !extra {
		return err
	}
	if len(q.Sort) > 0 || (page.Total >= q.Offset && len(page.Items) < q.Limit) {
		page.Items = append(page.Items, p)
	}
	page.Total++
	return nil
}

// Custom sorts scan and retain matching rows inside one repeatable snapshot.
// Missing/null/wrong-type values sort last in either direction, then logical ID.
func sortPage(ctx context.Context, page *Page, q Query) error {
	type row struct {
		p    eventstore.Projection
		keys []*orderedValue
	}
	rows := make([]row, len(page.Items))
	for i, p := range page.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := decode(p.Body)
		if err != nil {
			return err
		}
		rows[i] = row{p: p, keys: make([]*orderedValue, len(q.Sort))}
		for j, s := range q.Sort {
			rows[i].keys[j] = sortValue(body, s)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		for k, s := range q.Sort {
			a, b := rows[i].keys[k], rows[j].keys[k]
			if a == nil && b == nil {
				continue
			}
			if a == nil {
				return false
			}
			if b == nil {
				return true
			}
			c := compare(*a, *b, s.Kind)
			if c == 0 {
				continue
			}
			if s.Desc {
				return c > 0
			}
			return c < 0
		}
		return rows[i].p.ID < rows[j].p.ID
	})
	if err := ctx.Err(); err != nil {
		return err
	}
	page.Items = []eventstore.Projection{}
	start := min(q.Offset, len(rows))
	end := start + min(q.Limit, len(rows)-start)
	for _, r := range rows[start:end] {
		page.Items = append(page.Items, r.p)
	}
	return nil
}

func (s *Store) scan(ctx context.Context, r eventstore.Reader, q Query, page *Page, maximum int) error {
	reader, ok := r.(ProjectionReader)
	if !ok {
		return fmt.Errorf("backend does not support projection listing")
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, err := reader.Projections(ctx, s.Scope.Network, s.Scope.Service, s.Scope.Domain, after, 256)
		if err != nil {
			return err
		}
		if len(rows) > 256 {
			return fmt.Errorf("backend exceeded projection page limit")
		}
		for _, p := range rows {
			if p.ID <= after {
				return fmt.Errorf("backend projection order did not advance")
			}
			after = p.ID
			id, ok := s.logicalID(p.ID)
			if !ok {
				continue
			}
			p.ID = id
			if err := s.collect(p, q, page); err != nil {
				return err
			}
			if maximum > 0 && page.Total > maximum {
				return invalid(fmt.Errorf("view selection exceeds maximum %d", maximum))
			}
		}
		if len(rows) < 256 {
			return nil
		}
	}
}
