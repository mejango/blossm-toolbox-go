//go:build integration

// Package querytest runs the same candidate contracts against each native adapter.
package querytest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func root(id string) eventstore.Root {
	return eventstore.Root{Network: "candidates", Service: "service", Domain: "domain", ID: id}
}
func predicate(path, value string) eventstore.Predicate {
	return eventstore.Predicate{Path: path, Value: json.RawMessage(value)}
}

func Candidates(t *testing.T, b eventstore.Backend, ctx context.Context) {
	t.Helper()
	states := map[string]string{
		"number":      `{"kind":"typed","n":2,"nullable":null,"flag":true}`,
		"string":      `{"kind":"typed","n":"2","nullable":"null","flag":"true"}`,
		"missing":     `{"kind":"typed"}`,
		"arrays":      `{"items":[{"x":1},{"y":"yes"}],"nested":[[[{"0":{"v":2}}]]],"list":[[[2]]]}`,
		"literal-dot": `{"a.b":7}`,
		"nested-dot":  `{"a":{"b":7}}`,
		"dirty":       `{"kind":"never"}`,
		"latest":      `{"kind":"latest"}`,
	}
	for i := 0; i < 150; i++ {
		states[fmt.Sprintf("bulk-%03d", i)] = `{"kind":"bulk","a":1,"b":"yes"}`
	}
	for i := 0; i < 20; i++ {
		states[fmt.Sprintf("reject-%03d", i)] = `{"kind":"bulk","a":1,"b":"no"}`
	}
	roots := []eventstore.Root{root("unsnapshotted")}
	for name := range states {
		roots = append(roots, root(name))
	}
	other := root("foreign")
	other.Network = "other"
	roots = append(roots, other)
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.LockRoots(ctx, roots); err != nil {
			return err
		}
		if err := tx.PutCount(ctx, root("unsnapshotted"), 1); err != nil {
			return err
		}
		if err := tx.PutCount(ctx, other, 1); err != nil {
			return err
		}
		for name, state := range states {
			r := root(name)
			number, last, count := int64(0), int64(0), int64(1)
			if name == "latest" {
				number = 10
				last = 10
				count = 11
			}
			if name == "dirty" {
				count = 2
			}
			if err := tx.PutCount(ctx, r, count); err != nil {
				return err
			}
			snap := eventstore.Snapshot{Root: r, Number: number, LastEvent: last, State: json.RawMessage(state), Hash: name}
			if err := tx.PutSnapshot(ctx, snap); err != nil {
				return err
			}
			if name == "latest" {
				snap.Number = 2
				snap.LastEvent = 2
				snap.State = json.RawMessage(`{"kind":"obsolete"}`)
				snap.Hash = "old-latest"
				if err := tx.PutSnapshot(ctx, snap); err != nil {
					return err
				}
			}
		}
		return nil
	}))
	rollback := errors.New("rollback snapshot head")
	err := b.Update(ctx, func(tx eventstore.Tx) error {
		if err := tx.PutSnapshot(ctx, eventstore.Snapshot{Root: root("latest"), Number: 11, LastEvent: 10, State: json.RawMessage(`{"kind":"rolled-back"}`), Hash: "rolled-back"}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	checks := []struct {
		name string
		p    []eventstore.Predicate
		want []string
	}{
		{"conjunction-unbounded", []eventstore.Predicate{predicate("a", "1"), predicate("b", `"yes"`)}, nil},
		{"number", []eventstore.Predicate{predicate("n", "2.0")}, []string{"number"}},
		{"string", []eventstore.Predicate{predicate("n", `"2"`)}, []string{"string"}},
		{"null-not-missing", []eventstore.Predicate{predicate("nullable", "null")}, []string{"number"}},
		{"boolean", []eventstore.Predicate{predicate("flag", "true")}, []string{"number"}},
		{"split-array-elements", []eventstore.Predicate{predicate("items.x", "1"), predicate("items.y", `"yes"`)}, []string{"arrays"}},
		{"nested-array-numeric-key", []eventstore.Predicate{predicate("nested.0.v", "2")}, []string{"arrays"}},
		{"nested-array-leaf", []eventstore.Predicate{predicate("list", "2")}, []string{"arrays"}},
		{"literal-dot-not-nested", []eventstore.Predicate{predicate("a.b", "7")}, []string{"nested-dot"}},
		{"latest-number", []eventstore.Predicate{predicate("kind", `"latest"`)}, []string{"latest"}},
		{"old-snapshot-excluded", []eventstore.Predicate{predicate("kind", `"obsolete"`)}, nil},
		{"dirty-frontier", []eventstore.Predicate{predicate("derived", "3")}, nil},
	}
	for i := 0; i < 150; i++ {
		checks[0].want = append(checks[0].want, fmt.Sprintf("bulk-%03d", i))
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			must(t, b.View(ctx, func(r eventstore.Reader) error {
				candidates, ok := r.(eventstore.CandidateReader)
				if !ok {
					t.Fatal("candidate interface missing")
				}
				got, err := candidates.Candidates(ctx, "candidates", "service", "domain", tc.p)
				if err != nil {
					return err
				}
				ids := make([]string, 0, len(got))
				for _, r := range got {
					if r.Network != "candidates" {
						t.Fatal("scope leak")
					}
					ids = append(ids, r.ID)
				}
				sort.Strings(ids)
				want := append(append([]string{}, tc.want...), "dirty", "unsnapshotted")
				sort.Strings(want)
				if !reflect.DeepEqual(ids, want) {
					t.Fatalf("got %v want %v", ids, want)
				}
				return nil
			}))
		})
	}
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		all, err := r.(eventstore.CandidateReader).Candidates(ctx, "candidates", "service", "domain", nil)
		if len(all) != len(states)+1 {
			t.Fatalf("unbounded root-only query got%d want%d", len(all), len(states)+1)
		}
		return err
	}))
}

type mergeReducer struct{}

func (mergeReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return eventstore.Canonical(p)
}
func (mergeReducer) Apply(state json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	var a, p map[string]int
	if err := json.Unmarshal(state, &a); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(e.Input.Payload, &p); err != nil {
		return nil, err
	}
	for k, v := range p {
		a[k] = v
	}
	a["derived"] = a["a"] + a["b"]
	return json.Marshal(a)
}

func Replay(t *testing.T, b eventstore.Backend, ctx context.Context) {
	store := eventstore.Store{Backend: b, Local: true, Reducers: map[string]eventstore.Reducer{"service/domain": mergeReducer{}}}
	input := func(id, key, payload string) eventstore.Input {
		r := root(id)
		r.Network = "replay"
		return eventstore.Input{Root: r, Key: key, Action: "set", Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(payload), Transaction: eventstore.Transaction{ID: key, Issued: "2026-11-12T00:00:00Z"}}
	}
	_, err := store.Append(ctx, []eventstore.Input{input("split", "split-a", `{"a":1}`), input("clean", "clean-a", `{"a":1,"b":2}`), input("nonmatch", "nonmatch-a", `{"a":9,"b":9}`)})
	must(t, err)
	_, err = store.BuildBlock(ctx, 10, nil)
	must(t, err)
	_, err = store.Append(ctx, []eventstore.Input{input("split", "split-b", `{"b":2}`), input("unsnapped", "new-a", `{"a":1,"b":2}`)})
	must(t, err)
	for _, pred := range [][]eventstore.Predicate{{predicate("derived", "3")}, {predicate("a", "1"), predicate("b", "2")}} {
		got, err := store.Query(ctx, "replay", "service", "domain", pred)
		must(t, err)
		ids := []string{}
		for _, a := range got {
			ids = append(ids, a.Root.ID)
		}
		sort.Strings(ids)
		if !reflect.DeepEqual(ids, []string{"clean", "split", "unsnapped"}) {
			t.Fatalf("replay candidates omitted derived/split state: %v", ids)
		}
	}
}

type projectionReader interface {
	Projections(context.Context, string, string, string, string, int) ([]eventstore.Projection, error)
}

func Projections(t *testing.T, b eventstore.Backend, ctx context.Context) {
	must(t, b.Update(ctx, func(tx eventstore.Tx) error {
		for i := 0; i < 140; i++ {
			r := root("source")
			if i == 139 {
				r.Network = "other"
			}
			_, err := tx.PutProjection(ctx, eventstore.Projection{ID: fmt.Sprintf("row-%03d", i), Root: r, Version: 1, Body: json.RawMessage(`{"owner":"owner"}`), Deleted: i == 0})
			if err != nil {
				return err
			}
		}
		return nil
	}))
	must(t, b.View(ctx, func(r eventstore.Reader) error {
		list, ok := r.(projectionReader)
		if !ok {
			t.Fatal("projection listing interface missing")
		}
		rows, err := list.Projections(ctx, "candidates", "service", "domain", "", 200)
		if err != nil {
			return err
		}
		if len(rows) != 138 || rows[0].ID != "row-001" || rows[137].ID != "row-138" {
			t.Fatal("scope/deletion/unbounded ordering failure", len(rows))
		}
		rows, err = list.Projections(ctx, "candidates", "service", "domain", "row-100", 2)
		if err != nil {
			return err
		}
		if len(rows) != 2 || rows[0].ID != "row-101" || rows[1].ID != "row-102" {
			t.Fatal("cursor/limit failure", rows)
		}
		_, err = list.Projections(ctx, "candidates", "service", "domain", "", 0)
		if err == nil {
			t.Fatal("zero limit allowed")
		}
		return nil
	}))
}
