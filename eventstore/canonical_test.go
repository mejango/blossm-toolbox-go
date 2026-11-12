package eventstore

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCanonical(t *testing.T) {
	for _, raw := range []string{`{"a":1,"a":2}`, `{"a":9007199254740992}`, `{} {}`, `NaN`, `"\ud800"`, `"\udc00"`, `[1,]`} {
		if _, e := Canonical([]byte(raw)); e == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	a, e := Canonical([]byte(` {"z":1.0,"a":[true,null,-0]} `))
	if e != nil {
		t.Fatal(e)
	}
	if string(a) != `{"a":[true,null,0],"z":1}` {
		t.Fatal(string(a))
	}
	b, e := Canonical(a)
	if e != nil || string(a) != string(b) {
		t.Fatal("not fixed point")
	}
	h, e := Digest(json.RawMessage(`{"a":1}`))
	if e != nil || h != "015abd7f5cc57a2dd94b7590f04ad8084273905ee33ec5cebeae62276a97f862" {
		t.Fatalf("hash vector %s %v", h, e)
	}
}
func TestPredicates(t *testing.T) {
	state := json.RawMessage(`{"roles":[{"subject":{"id":"a"}},{"subject":{"id":"b"}}],"n":1,"value":null}`)
	cases := []struct {
		p    []Predicate
		want bool
	}{{[]Predicate{{"roles.subject.id", json.RawMessage(`"b"`)}, {"n", json.RawMessage(`1`)}}, true}, {[]Predicate{{"n", json.RawMessage(`"1"`)}}, false}, {[]Predicate{{"value", json.RawMessage(`null`)}}, true}, {[]Predicate{{"missing", json.RawMessage(`null`)}}, false}}
	for _, tc := range cases {
		got, e := Matches(state, tc.p)
		if e != nil || got != tc.want {
			t.Fatalf("match %v %v", got, e)
		}
	}
}

func TestCanonicalTypedInteger(t *testing.T) {
	raw, e := Canonical([]byte(`{"expires":1802230840}`))
	if e != nil {
		t.Fatal(e)
	}
	var value struct {
		Expires int64 `json:"expires"`
	}
	if e = json.Unmarshal(raw, &value); e != nil || value.Expires != 1802230840 {
		t.Fatalf("typed integer %s %v", raw, e)
	}
}

func TestBlockHashBinding(t *testing.T) {
	b := Block{Number: 0, Events: []byte{0, 1, 255}, Snapshots: []byte{2, 3}, Transactions: []byte{4}}
	var e error
	b.Hash, e = Digest(b)
	if e != nil {
		t.Fatal(e)
	}
	if e = VerifyBlock(context.Background(), b, nil, nil); e != nil {
		t.Fatal(e)
	}
	b.Events[0] = 9
	if e = VerifyBlock(context.Background(), b, nil, nil); e == nil {
		t.Fatal("accepted changed block bytes")
	}
}
