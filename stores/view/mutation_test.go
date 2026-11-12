package view

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }
func predicate(path, value string) eventstore.Predicate {
	return eventstore.Predicate{Path: path, Value: raw(value)}
}
func TestComposeMutations(t *testing.T) {
	input := raw(`{"owner":"alice","n":1,"tags":["a"],"profile":{"old":true}}`)
	got, err := Apply(input, []Operation{
		{Kind: Set, Path: "profile.name", Value: raw(`"A"`)},
		{Kind: Unset, Path: "profile.old"},
		{Kind: Inc, Path: "n", Value: raw(`2`)},
		{Kind: Inc, Path: "new.n", Value: raw(`4`)},
		{Kind: Push, Path: "tags", Value: raw(`"b"`)},
		{Kind: AddToSet, Path: "tags", Value: raw(`"a"`)},
		{Kind: AddToSet, Path: "tags", Value: raw(`"c"`)},
		{Kind: Pull, Path: "tags", Value: raw(`"b"`)},
	})
	want := `{"n":3,"new":{"n":4},"owner":"alice","profile":{"name":"A"},"tags":["a","c"]}`
	if err != nil || string(got) != want {
		t.Fatalf("%s %v", got, err)
	}
	if string(input) != `{"owner":"alice","n":1,"tags":["a"],"profile":{"old":true}}` {
		t.Fatal("input mutated")
	}
}
func TestFilteredNestedComposition(t *testing.T) {
	filters := map[string][]eventstore.Predicate{"parent": {predicate("id", `"a"`)}, "child": {predicate("id", `"x"`)}}
	got, err := Apply(raw(`{}`), []Operation{
		{Kind: Set, Path: "parents", Value: raw(`[{"id":"a","children":[{"id":"x","n":1},{"id":"y","n":2}]},{"id":"b","children":[{"id":"x","n":3}]},{"children":[]}]`)},
		{Kind: Inc, Path: "parents.$[parent].children.$[child].n", Value: raw(`5`), Filters: filters},
		{Kind: Set, Path: "parents.$[].object", Value: raw(`{"v":1}`)},
		{Kind: Set, Path: "parents.0.object.v", Value: raw(`9`)},
	})
	want := `{"parents":[{"children":[{"id":"x","n":6},{"id":"y","n":2}],"id":"a","object":{"v":9}},{"children":[{"id":"x","n":3}],"id":"b","object":{"v":1}},{"children":[],"object":{"v":1}}]}`
	if err != nil || string(got) != want {
		t.Fatalf("nested composition or sibling alias: %s %v", got, err)
	}
}
func TestPullObjectsAndCanonicalSet(t *testing.T) {
	got, err := Apply(raw(`{"items":[{"id":"a","n":1},{"id":"b","n":2}],"set":[{"a":1,"b":2}]}`), []Operation{
		{Kind: Pull, Path: "items", Where: []eventstore.Predicate{predicate("id", `"a"`)}},
		{Kind: AddToSet, Path: "set", Value: raw(`{"b":2,"a":1.0}`)},
		{Kind: Unset, Path: "items.0.n"},
	})
	if err != nil || string(got) != `{"items":[{"id":"b"}],"set":[{"a":1,"b":2}]}` {
		t.Fatalf("%s %v", got, err)
	}
}
func TestMutationRejectsInvalidAndDoesNotCoerce(t *testing.T) {
	cases := []struct {
		body string
		op   Operation
	}{
		{`[]`, Operation{Kind: Set, Path: "x", Value: raw(`1`)}},
		{`{}`, Operation{Kind: "rename", Path: "x", Value: raw(`1`)}},
		{`{}`, Operation{Kind: Set, Path: "a..b", Value: raw(`1`)}},
		{`{}`, Operation{Kind: Set, Path: "a", Value: raw(`{"x":1,"x":2}`)}},
		{`{"n":"1"}`, Operation{Kind: Inc, Path: "n", Value: raw(`1`)}},
		{`{"n":null}`, Operation{Kind: Inc, Path: "n", Value: raw(`1`)}},
		{`{}`, Operation{Kind: Inc, Path: "n", Value: raw(`"1"`)}},
		{`{"n":9007199254740991}`, Operation{Kind: Inc, Path: "n", Value: raw(`1`)}},
		{`{"a":null}`, Operation{Kind: Push, Path: "a", Value: raw(`1`)}},
		{`{"a":1}`, Operation{Kind: Pull, Path: "a", Value: raw(`1`)}},
		{`{"a":null}`, Operation{Kind: Pull, Path: "a", Value: raw(`1`)}},
		{`{"a":[]}`, Operation{Kind: Set, Path: "a.0", Value: raw(`1`)}},
		{`{"a":1}`, Operation{Kind: Set, Path: "a.x", Value: raw(`1`)}},
		{`{"a":[]}`, Operation{Kind: Set, Path: "a.$[x].n", Value: raw(`1`)}},
		{`{}`, Operation{Kind: Set, Path: "x", Value: raw(`1`), Filters: map[string][]eventstore.Predicate{"unused": {predicate("x", `1`)}}}},
		{`{}`, Operation{Kind: Unset, Path: "x", Value: raw(`1`)}},
		{`{"a":[]}`, Operation{Kind: Pull, Path: "a", Value: raw(`1`), Where: []eventstore.Predicate{predicate("id", `1`)}}},
	}
	for i, c := range cases {
		t.Run(strings.Join([]string{string(c.op.Kind), c.op.Path, c.body}, "/"), func(t *testing.T) {
			if got, err := Apply(raw(c.body), []Operation{c.op}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("case %d accepted: %s", i, got)
			}
		})
	}
}
func TestMutationFailureDoesNotPartiallyModifyInput(t *testing.T) {
	body := raw(`{"n":1}`)
	if got, err := Apply(body, []Operation{{Kind: Set, Path: "n", Value: raw(`9`)}, {Kind: Push, Path: "n", Value: raw(`1`)}}); err == nil || got != nil {
		t.Fatal(got, err)
	}
	if string(body) != `{"n":1}` {
		t.Fatal(string(body))
	}
}
