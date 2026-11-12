package query

import (
	"encoding/json"
	"testing"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

func TestPredicates(t *testing.T) {
	state := json.RawMessage(`{"roles":[{"subject":{"id":"a"}},{"subject":{"id":"b"}}],"n":1,"value":null}`)
	cases := []struct {
		p    []eventlib.Predicate
		want bool
	}{{[]eventlib.Predicate{{Path: "roles.subject.id", Value: json.RawMessage(`"b"`)}, {Path: "n", Value: json.RawMessage(`1`)}}, true}, {[]eventlib.Predicate{{Path: "n", Value: json.RawMessage(`"1"`)}}, false}, {[]eventlib.Predicate{{Path: "value", Value: json.RawMessage(`null`)}}, true}, {[]eventlib.Predicate{{Path: "missing", Value: json.RawMessage(`null`)}}, false}}
	for _, tc := range cases {
		got, e := Matches(state, tc.p)
		if e != nil || got != tc.want {
			t.Fatalf("match %v %v", got, e)
		}
	}
}
