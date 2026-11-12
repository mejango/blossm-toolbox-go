package view

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mejango/blossm-toolbox-go/eventstore"
)

func queryStore(bodies ...string) Store {
	r := &queryReader{}
	for i, b := range bodies {
		r.rows = append(r.rows, eventstore.Projection{ID: string(rune('a' + i)), Root: root("r"), Body: raw(b)})
	}
	return Store{Scope: scope(), Backend: queryBackend{reader: r}}
}
func ids(page Page) []string {
	out := []string{}
	for _, p := range page.Items {
		out = append(out, p.ID)
	}
	return out
}
func bound(value string, exclusive bool) *Bound {
	return &Bound{Value: raw(value), Exclusive: exclusive}
}
func TestTypedRangesDateTextSortPagination(t *testing.T) {
	s := queryStore(
		`{"owner":"alice","n":2,"date":"2026-01-01T01:00:00+01:00","title":"ALPHA beta"}`,
		`{"owner":"alice","n":3,"date":"2026-01-02T00:00:00Z","title":"alphabet"}`,
		`{"owner":"alice","n":"2","date":"bad","title":"alpha"}`,
		`{"owner":"bob","n":2,"date":"2026-01-01T00:00:00Z","title":"alpha"}`,
		`{"owner":"alice","n":[1,2],"date":"2026-01-01T00:00:00Z","title":"alpha"}`,
	)
	q := Query{Ranges: []Range{{Path: "n", Kind: NumberValue, Lower: bound(`1`, true), Upper: bound(`3`, true)}, {Path: "date", Kind: DateValue, Lower: bound(`"2026-01-01T00:00:00Z"`, false), Upper: bound(`"2026-01-01T00:00:00Z"`, false)}}, Text: &TextQuery{Paths: []string{"title"}, Value: "AlPhA"}, Sort: []Sort{{Path: "n", Kind: NumberValue, Desc: true}}, Limit: 1}
	page, err := s.List(context.Background(), q)
	if err != nil || !reflect.DeepEqual(ids(page), []string{"a"}) || page.Total != 2 || page.NextOffset == nil || *page.NextOffset != 1 {
		t.Fatal(page, err)
	}
	q.Offset = 1
	page, err = s.List(context.Background(), q)
	if err != nil || !reflect.DeepEqual(ids(page), []string{"e"}) || page.Total != 2 || page.NextOffset != nil {
		t.Fatal(page, err)
	}
	q = Query{Text: &TextQuery{Paths: []string{"title"}, Value: ".*"}}
	page, err = s.List(context.Background(), q)
	if err != nil || page.Total != 0 {
		t.Fatal("text interpreted as regex", page, err)
	}
	q.Text.Value = "a"
	q.Text.Paths = []string{"missing"}
	page, err = s.List(context.Background(), q)
	if err != nil || !reflect.DeepEqual(ids(page), []string{"a"}) {
		t.Fatal("exact logical id search", page, err)
	}
}
func TestTypedSortArraysMissingAndTies(t *testing.T) {
	s := queryStore(`{"owner":"alice","items":[{"n":3},{"n":1}]}`, `{"owner":"alice","items":[{"n":2}]}`, `{"owner":"alice","items":[{"n":"0"}]}`, `{"owner":"alice"}`, `{"owner":"alice","items":[{"n":null}]}`, `{"owner":"alice","items":[{"n":2}]}`)
	for _, tc := range []struct {
		desc bool
		want []string
	}{{false, []string{"a", "b", "f", "c", "d", "e"}}, {true, []string{"a", "b", "f", "c", "d", "e"}}} {
		page, err := s.List(context.Background(), Query{Sort: []Sort{{Path: "items.n", Kind: NumberValue, Desc: tc.desc}}})
		if err != nil || !reflect.DeepEqual(ids(page), tc.want) {
			t.Fatal(page, err)
		}
	}
	s = queryStore(`{"owner":"alice","n":1,"title":"b"}`, `{"owner":"alice","n":1,"title":"a"}`, `{"owner":"alice","n":2,"title":"z"}`)
	page, err := s.List(context.Background(), Query{Sort: []Sort{{Path: "n", Kind: NumberValue}, {Path: "title", Kind: StringValue}}})
	if err != nil || !reflect.DeepEqual(ids(page), []string{"b", "a", "c"}) {
		t.Fatal(page, err)
	}
}
func TestExtendedQueryInvalidBeforeBackend(t *testing.T) {
	s := Store{Scope: scope()}
	for _, q := range []Query{
		{Ranges: []Range{{Path: "x", Kind: NumberValue}}},
		{Ranges: []Range{{Path: "x", Kind: NumberValue, Lower: bound(`"1"`, false)}}},
		{Ranges: []Range{{Path: "x", Kind: DateValue, Lower: bound(`"2026-01-01"`, false)}}},
		{Ranges: []Range{{Path: "x", Kind: NumberValue, Lower: bound(`2`, false), Upper: bound(`1`, false)}}},
		{Ranges: []Range{{Path: "x..a", Kind: NumberValue, Lower: bound(`1`, false)}}},
		{Text: &TextQuery{Value: "x"}}, {Text: &TextQuery{Paths: []string{"a"}}},
		{Sort: []Sort{{Path: "n", Kind: "automatic"}}}, {Sort: []Sort{{Path: "a..b", Kind: NumberValue}}},
	} {
		if _, err := s.List(context.Background(), q); !errors.Is(err, ErrInvalid) {
			t.Fatal(q, err)
		}
	}
}
func TestOwnerAnyIsUnionUnderCommonAuthority(t *testing.T) {
	s := queryStore(`{"owner":"alice","groups":["g1"]}`, `{"owner":"alice","groups":["g2"]}`, `{"owner":"alice","groups":["g3"]}`, `{"owner":"bob","groups":["g1"]}`)
	s.Scope.OwnerAny = [][]eventstore.Predicate{{predicate("groups", `"g1"`)}, {predicate("groups", `"g2"`)}}
	page, err := s.List(context.Background(), Query{})
	if err != nil || !reflect.DeepEqual(ids(page), []string{"a", "b"}) {
		t.Fatal(page, err)
	}
	page, err = s.List(context.Background(), Query{Predicates: []eventstore.Predicate{predicate("owner", `"bob"`)}})
	if err != nil || page.Total != 0 {
		t.Fatal(page, err)
	}
	s.Scope.Owner = nil
	page, err = s.List(context.Background(), Query{})
	if err != nil || page.Total != 3 {
		t.Fatal(page, err)
	}
	s.Scope.OwnerAny = [][]eventstore.Predicate{{}}
	if _, err = s.List(context.Background(), Query{}); err == nil {
		t.Fatal("empty group grants access")
	}
	original := scope()
	original.OwnerAny = [][]eventstore.Predicate{{predicate("groups", `"g1"`)}}
	copy := copyScope(original)
	copy.OwnerAny[0][0].Value[1] = 'X'
	copy.OwnerAny[0][0].Path = "other"
	if original.OwnerAny[0][0].Path != "groups" || string(original.OwnerAny[0][0].Value) != `"g1"` {
		t.Fatal("authority alias")
	}
}
