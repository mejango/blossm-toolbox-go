package view

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"blossm.local/backend/eventstore"
)

type componentRows struct{ *queryReader }

func (r componentRows) Projection(_ context.Context, id string) (eventstore.Projection, error) {
	for _, p := range r.rows {
		if p.ID == id {
			return p, nil
		}
	}
	return eventstore.Projection{}, eventstore.ErrNotFound
}
func namedBackend() map[string]ComponentReader {
	rows := componentRows{&queryReader{rows: []eventstore.Projection{
		{ID: "a", Root: root("r"), Version: 3, Body: raw(`{"owner":"alice","n":1}`)},
		{ID: "b", Root: root("r"), Version: 4, Body: raw(`{"owner":"bob","n":2}`)},
		{ID: "c", Root: root("r"), Version: 5, Body: raw(`{"owner":"alice","n":3}`), Deleted: true},
	}}}
	return map[string]ComponentReader{"summary": BackendComponent{Backend: queryBackend{reader: rows}}, "details": BackendComponent{Backend: queryBackend{reader: rows}}}
}
func TestCompositeTypedQueryResponseAndOwnerFiltering(t *testing.T) {
	type request struct {
		ID     string
		Filter Query
	}
	type response struct {
		Selected string
		Total    int
		Version  int64
		Custom   []string
	}
	query := request{ID: "a", Filter: Query{Limit: 10}}
	got, err := ExecuteComposite(context.Background(), scope(), query, namedBackend(), func(ctx context.Context, q request, auth Scope, views *Components) (response, error) {
		if !reflect.DeepEqual(q, query) || !reflect.DeepEqual(auth, scope()) {
			t.Fatal("query or auth altered")
		}
		p, err := views.Get("details", q.ID)
		if err != nil {
			return response{}, err
		}
		page, err := views.List("summary", q.Filter)
		if err != nil {
			return response{}, err
		}
		if len(page.Items) != 1 || page.Items[0].ID != "a" || page.NextOffset != nil {
			t.Fatal("owner or tombstone leaked", page)
		}
		if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		return response{Selected: p.ID, Total: page.Total, Version: p.Version, Custom: []string{"application", "response"}}, nil
	})
	want := response{Selected: "a", Total: 1, Version: 3, Custom: []string{"application", "response"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal(got, err)
	}
}
func TestCompositeScopeCannotBeOverridden(t *testing.T) {
	caller := scope()
	readers := namedBackend()
	type request struct {
		Scope  Scope
		Filter Query
	}
	forged := scope()
	forged.Owner = []eventstore.Predicate{predicate("owner", `"bob"`)}
	q := request{Scope: forged, Filter: Query{Predicates: forged.Owner}}
	_, err := ExecuteComposite(context.Background(), caller, q, readers, func(_ context.Context, q request, auth Scope, views *Components) (bool, error) {
		// None of query data, the visible auth copy, caller-owned bytes, or the
		// original registry may rewrite the authority already bound to readers.
		auth.Owner[0].Value[1] = 'X'
		auth.Network = "forged"
		caller.Owner[0].Value[1] = 'Y'
		readers["summary"] = nil
		page, err := views.List("summary", q.Filter)
		if err != nil {
			return false, err
		}
		if page.Total != 0 {
			t.Fatal("query replaced owner", page)
		}
		if _, err := views.Get("details", "b"); !errors.Is(err, eventstore.ErrNotFound) {
			t.Fatal("cross-owner Get", err)
		}
		page, err = views.List("summary", Query{})
		if err != nil {
			return false, err
		}
		if page.Total != 1 || page.Items[0].ID != "a" {
			t.Fatal("scope alias leaked", page)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

type componentFuncs struct {
	get  func(context.Context, Scope, string) (eventstore.Projection, error)
	list func(context.Context, Scope, Query) (Page, error)
}

func (r componentFuncs) Get(ctx context.Context, s Scope, id string) (eventstore.Projection, error) {
	if r.get == nil {
		return eventstore.Projection{}, errors.New("unexpected get")
	}
	return r.get(ctx, s, id)
}
func (r componentFuncs) List(ctx context.Context, s Scope, q Query) (Page, error) {
	if r.list == nil {
		return Page{}, errors.New("unexpected list")
	}
	return r.list(ctx, s, q)
}
func TestCompositeComponentAndMainErrors(t *testing.T) {
	sentinel := errors.New("component failed")
	readers := map[string]ComponentReader{"failed": componentFuncs{
		get: func(context.Context, Scope, string) (eventstore.Projection, error) {
			return eventstore.Projection{}, sentinel
		},
		list: func(context.Context, Scope, Query) (Page, error) { return Page{}, sentinel },
	}}
	for _, kind := range []string{"get", "list", "main"} {
		got, err := ExecuteComposite(context.Background(), scope(), kind, readers, func(_ context.Context, kind string, _ Scope, views *Components) (string, error) {
			err := sentinel
			if kind == "get" {
				_, err = views.Get("failed", "a")
			}
			if kind == "list" {
				_, err = views.List("failed", Query{})
			}
			return "partial response must be discarded", err
		})
		if got != "" || !errors.Is(err, sentinel) {
			t.Fatal(kind, got, err)
		}
	}
}
func TestCompositeRejectsReaderScopeViolations(t *testing.T) {
	reads := 0
	readers := map[string]ComponentReader{"unsafe": componentFuncs{
		get: func(_ context.Context, s Scope, _ string) (eventstore.Projection, error) {
			reads++
			s.Owner[0].Value[1] = 'X'
			return eventstore.Projection{ID: "b", Root: root("r"), Body: raw(`{"owner":"bob"}`)}, nil
		},
		list: func(context.Context, Scope, Query) (Page, error) {
			reads++
			return Page{Total: 1, Items: []eventstore.Projection{{ID: "c", Root: root("r"), Body: raw(`{"owner":"alice"}`), Deleted: true}}}, nil
		},
	}}
	_, err := ExecuteComposite(context.Background(), scope(), struct{}{}, readers, func(_ context.Context, _ struct{}, _ Scope, views *Components) (bool, error) {
		if _, err := views.Get("unsafe", "b"); err == nil {
			t.Fatal("component scope violation exposed")
		}
		if _, err := views.List("unsafe", Query{}); err == nil {
			t.Fatal("component tombstone exposed")
		}
		if _, err := views.Get("missing", "a"); err == nil {
			t.Fatal("unknown component accepted")
		}
		return true, nil
	})
	if err != nil || reads != 2 {
		t.Fatal(reads, err)
	}
}
func TestCompositeCancellationAndReaderLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	_, err := ExecuteComposite(ctx, scope(), true, namedBackend(), func(context.Context, bool, Scope, *Components) (bool, error) { called = true; return true, nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatal(called, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()
	var retained *Components
	readers := map[string]ComponentReader{"cancel": componentFuncs{get: func(ctx context.Context, _ Scope, _ string) (eventstore.Projection, error) {
		cancel()
		<-ctx.Done()
		return eventstore.Projection{ID: "a", Root: root("r"), Body: raw(`{"owner":"alice"}`)}, nil
	}}}
	got, err := ExecuteComposite(ctx, scope(), true, readers, func(_ context.Context, _ bool, _ Scope, views *Components) (bool, error) {
		retained = views
		_, err := views.Get("cancel", "a")
		return true, err
	})
	if got || !errors.Is(err, context.Canceled) {
		t.Fatal(got, err)
	}
	if _, err := retained.Get("cancel", "a"); !errors.Is(err, context.Canceled) {
		t.Fatal("reader survived cancellation", err)
	}
	_, err = ExecuteComposite(context.Background(), scope(), true, namedBackend(), func(_ context.Context, _ bool, _ Scope, views *Components) (bool, error) {
		retained = views
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retained.List("summary", Query{}); !errors.Is(err, context.Canceled) {
		t.Fatal("reader survived execution", err)
	}
}
func TestCompositeValidationAndContextPropagation(t *testing.T) {
	main := func(context.Context, bool, Scope, *Components) (bool, error) {
		t.Fatal("invalid execution invoked main")
		return false, nil
	}
	badScope := scope()
	badScope.Owner = nil
	if _, err := ExecuteComposite(context.Background(), badScope, true, namedBackend(), main); err == nil {
		t.Fatal("empty owner scope accepted")
	}
	for _, readers := range []map[string]ComponentReader{{"": BackendComponent{}}, {"nil": nil}} {
		if _, err := ExecuteComposite(context.Background(), scope(), true, readers, main); err == nil {
			t.Fatal("invalid component accepted")
		}
	}
	if _, err := ExecuteComposite[bool, bool](context.Background(), scope(), true, namedBackend(), nil); err == nil {
		t.Fatal("nil main accepted")
	}
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "caller-trace")
	readers := map[string]ComponentReader{"trace": componentFuncs{list: func(ctx context.Context, _ Scope, _ Query) (Page, error) {
		if ctx.Value(key{}) != "caller-trace" {
			t.Fatal("reader context lost")
		}
		return Page{}, nil
	}}}
	_, err := ExecuteComposite(ctx, scope(), true, readers, func(ctx context.Context, _ bool, _ Scope, views *Components) (bool, error) {
		if ctx.Value(key{}) != "caller-trace" {
			t.Fatal("main context lost")
		}
		_, err := views.List("trace", Query{})
		return true, err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCompositeRejectsSuccessAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := ExecuteComposite(ctx, scope(), "query", namedBackend(), func(context.Context, string, Scope, *Components) (string, error) {
		cancel()
		return "must not escape", nil
	})
	if result != "" || !errors.Is(err, context.Canceled) {
		t.Fatal(result, err)
	}
}
