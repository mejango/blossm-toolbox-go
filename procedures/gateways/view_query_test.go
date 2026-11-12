package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

func TestViewNamedIsolationSameRootIDVersion(t *testing.T) {
	b := gatewayFixture()
	h, err := (ViewGateway{Authorize: gatewayAuth, Stores: map[string]eventlib.Backend{"items": b, "second": b}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	body := `{"mutations":[{"id":"a","root":{"network":"n","service":"s","domain":"d","id":"a"},"version":1,"body":{"owner":"alice","n":99}}]}`
	w := gatewayRequest(h, "POST", "/view-stores/second/batch", body, true, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, name := range []string{"items", "second"} {
		w = gatewayRequest(h, "GET", "/view-stores/"+name+"/a", "", true, false)
		var p eventlib.Projection
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || w.Code != 200 || p.ID != "a" || p.Version != 1 {
			t.Fatal(p, w.Code, err)
		}
		want := `"n":1`
		if name == "second" {
			want = `"n":99`
		}
		if !strings.Contains(string(p.Body), want) {
			t.Fatal(string(p.Body))
		}
		w = gatewayRequest(h, "GET", "/view-stores/"+name, "", true, false)
		var page view.Page
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || page.Total != 1 || page.Items[0].ID != "a" {
			t.Fatal(page, err)
		}
	}
	tombstone := `{"mutations":[{"id":"a","root":{"network":"n","service":"s","domain":"d","id":"a"},"version":2,"tombstone":true,"body":{"owner":"alice"}}]}`
	w = gatewayRequest(h, "POST", "/view-stores/second/batch", tombstone, true, true)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = gatewayRequest(h, "GET", "/view-stores/second/a", "", true, false); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w = gatewayRequest(h, "GET", "/view-stores/items/a", "", true, false); w.Code != 200 {
		t.Fatal(w.Code)
	}
	unnamed := view.Store{Backend: b, Scope: gatewayScope()}
	if _, err := unnamed.Get(context.Background(), view.PhysicalID("items", "a")); !errors.Is(err, view.ErrInvalid) {
		t.Fatal("reserved prefix accepted", err)
	}
	page, err := unnamed.List(context.Background(), view.Query{})
	if err != nil || page.Total != 0 {
		t.Fatal("named rows leaked unnamed", page, err)
	}
}
func TestViewQueryRoundtripTextPaginationGroups(t *testing.T) {
	b := gatewayFixture()
	b.rows = map[string]eventlib.Projection{}
	for i, body := range []string{`{"owner":"alice","groups":["g1"],"n":2,"title":"Alpha"}`, `{"owner":"alice","groups":["g2"],"n":3,"title":"alphabet"}`, `{"owner":"alice","groups":["other"],"n":4,"title":"alpha"}`, `{"owner":"bob","groups":["g1"],"n":5,"title":"alpha"}`} {
		id := string(rune('a' + i))
		physical := view.PhysicalID("items", id)
		b.rows[physical] = eventlib.Projection{ID: physical, Root: gatewayRoot(id), Version: 1, Body: json.RawMessage(body)}
	}
	auth := func(r *http.Request, op, name string) (view.Scope, error) {
		s, err := gatewayAuth(r, op, name)
		s.OwnerAny = [][]eventlib.Predicate{{{Path: "groups", Value: json.RawMessage(`"g1"`)}}, {{Path: "groups", Value: json.RawMessage(`"g2"`)}}}
		return s, err
	}
	h, err := (ViewGateway{Authorize: auth, Stores: map[string]eventlib.Backend{"items": b}, Updates: map[string]func(context.Context, view.Scope, view.Query) (string, error){"items": func(context.Context, view.Scope, view.Query) (string, error) { return "/updates/items", nil }}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	q := view.Query{Text: &view.TextQuery{Paths: []string{"title"}, Value: "AlPhA"}, Ranges: []view.Range{{Path: "n", Kind: view.NumberValue, Lower: &view.Bound{Value: json.RawMessage(`1`), Exclusive: true}}}, Sort: []view.Sort{{Path: "n", Kind: view.NumberValue, Desc: true}}, Limit: 1}
	encoded, _ := json.Marshal(q)
	w := gatewayRequest(h, "GET", "/view-stores/items?query="+url.QueryEscape(string(encoded)), "", true, false)
	var page view.Page
	if err := canonicaljson.Decode(w.Body.Bytes(), &page); err != nil || w.Code != 200 || page.Total != 2 || page.Items[0].ID != "b" || page.Next == "" || page.Updates != "/updates/items" {
		t.Fatal(page, w.Code, err)
	}
	next, err := url.Parse(page.Next)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip view.Query
	if err := canonicaljson.Decode([]byte(next.Query().Get("query")), &roundtrip); err != nil || roundtrip.Text.Value != "AlPhA" || len(roundtrip.Ranges) != 1 || !roundtrip.Sort[0].Desc || roundtrip.Offset != 1 {
		t.Fatal(roundtrip, err)
	}
	w = gatewayRequest(h, "GET", page.Next, "", true, false)
	page = view.Page{}
	if err := canonicaljson.Decode(w.Body.Bytes(), &page); err != nil || page.Items[0].ID != "a" || page.Next != "" || page.Total != 2 {
		t.Fatal(page, err)
	}
	for _, attack := range []string{`{"OwnerAny":[[]]}`, `{"Name":"second"}`, `{"Predicates":[{"path":"owner","value":"bob"}]}`} {
		w = gatewayRequest(h, "GET", "/view-stores/items?query="+url.QueryEscape(attack), "", true, false)
		if w.Code == 200 {
			var p view.Page
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Total != 0 {
				t.Fatal("scope widened", p, err)
			}
		} else if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestViewBootstrapEmptyCSVHooks(t *testing.T) {
	b := gatewayFixture()
	cfg := ViewGateway{Authorize: gatewayAuth, Stores: map[string]eventlib.Backend{"items": b}, Bootstrap: map[string]func(context.Context, view.Scope) (string, error){"items": func(_ context.Context, s view.Scope) (string, error) {
		s.Owner[0].Value = json.RawMessage(`"bob"`)
		return "b", nil
	}}, Empty: map[string]func(context.Context, view.Scope, view.Query) (json.RawMessage, error){"items": func(context.Context, view.Scope, view.Query) (json.RawMessage, error) {
		return json.RawMessage(`{"message":"empty"}`), nil
	}}, CSV: map[string]ViewCSV{"items": {Columns: []view.Column{{Name: "n", Path: "n"}}, Format: func(eventlib.Projection) (json.RawMessage, error) { return json.RawMessage(`{"n":"=SUM(1,2)"}`), nil }}}}
	h, err := cfg.Handler()
	if err != nil {
		t.Fatal(err)
	}
	w := gatewayRequest(h, "GET", "/view-bootstrap/items", "", true, false)
	if w.Code != 404 {
		t.Fatal("bootstrap escaped owner", w.Code, w.Body.String())
	}
	cfg.Bootstrap["items"] = func(context.Context, view.Scope) (string, error) { return "a", nil }
	h, err = cfg.Handler()
	if err != nil {
		t.Fatal(err)
	}
	if w = gatewayRequest(h, "GET", "/view-bootstrap/items", "", true, false); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = gatewayRequest(h, "GET", "/view-bootstrap/items?query={}", "", true, false); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w = gatewayRequest(h, "GET", "/view-bootstrap/items", "", false, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w = gatewayRequest(h, "GET", "/view-stores/items?query="+url.QueryEscape(`{"Predicates":[{"path":"n","value":999}]}`), "", true, false)
	var page view.Page
	if err := canonicaljson.Decode(w.Body.Bytes(), &page); err != nil || page.Total != 0 || string(page.Empty) != `{"message":"empty"}` {
		t.Fatal(page, err)
	}
	w = gatewayRequest(h, "GET", "/view-stores/items?download=csv", "", true, false)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/csv") || w.Body.String() != "n,id\n\"'=SUM(1,2)\",a\n" {
		t.Fatal(w.Code, w.Body.String())
	}
	cfg.CSV["items"] = ViewCSV{Format: func(eventlib.Projection) (json.RawMessage, error) {
		return nil, errors.New("private formatter failure")
	}}
	h, err = cfg.Handler()
	if err != nil {
		t.Fatal(err)
	}
	w = gatewayRequest(h, "GET", "/view-stores/items?download=csv", "", true, false)
	if w.Code != 500 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Header().Get("Content-Type"), "csv") {
		t.Fatal(w.Code, w.Body.String())
	}
}
