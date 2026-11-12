package queryindex

import (
	"encoding/json"
	"testing"

	"blossm.local/backend/eventstore"
)

func TestTermsMatchSharedTraversal(t *testing.T) {
	states := []string{`{"n":2,"s":"2","nil":null,"bool":true}`, `{"a":[[[{"0":{"v":2}}]]],"items":[{"x":1},{"y":"yes"}],"list":[[[2]]]}`, `{"a.b":2,"a":{"b":3},"":{"x":4}}`, `{}`}
	predicates := [][]eventstore.Predicate{
		{{Path: "n", Value: json.RawMessage(`2.0`)}}, {{Path: "s", Value: json.RawMessage(`2`)}}, {{Path: "nil", Value: json.RawMessage(`null`)}}, {{Path: "missing", Value: json.RawMessage(`null`)}},
		{{Path: "a.0.v", Value: json.RawMessage(`2`)}}, {{Path: "items.x", Value: json.RawMessage(`1`)}, {Path: "items.y", Value: json.RawMessage(`"yes"`)}}, {{Path: "list", Value: json.RawMessage(`2`)}},
		{{Path: "a.b", Value: json.RawMessage(`2`)}}, {{Path: "a.b", Value: json.RawMessage(`3`)}}, {{Path: ".x", Value: json.RawMessage(`4`)}},
	}
	for _, raw := range states {
		state, err := eventstore.Canonical([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		terms, err := State(state)
		if err != nil {
			t.Fatal(err)
		}
		set := map[string]bool{}
		for _, term := range terms {
			set[term] = true
		}
		for _, pred := range predicates {
			want, err := eventstore.Matches(state, pred)
			if err != nil {
				if _, indexErr := Predicates(pred); indexErr == nil {
					t.Fatal("index accepted invalid predicate", pred)
				}
				continue
			}
			search, err := Predicates(pred)
			if err != nil {
				t.Fatal(err)
			}
			got := true
			for _, term := range search {
				got = got && set[term]
			}
			if got != want {
				t.Fatalf("%s %#v indexed=%v Matches=%v", raw, pred, got, want)
			}
		}
	}
}

func TestNilStateUsesWireNull(t *testing.T) {
	wire, err := json.Marshal(eventstore.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var decoded eventstore.Snapshot
	if err = json.Unmarshal(wire, &decoded); err != nil {
		t.Fatal(err)
	}
	nilTerms, err := State(nil)
	if err != nil {
		t.Fatal(err)
	}
	wireTerms, err := State(decoded.State)
	if err != nil {
		t.Fatal(err)
	}
	if len(nilTerms) != 1 || len(wireTerms) != 1 || nilTerms[0] != wireTerms[0] {
		t.Fatal("nil state differs from its persisted JSON null", nilTerms, wireTerms)
	}
}
