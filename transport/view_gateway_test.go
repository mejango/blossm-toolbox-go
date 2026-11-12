package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/fact"
	"blossm.local/backend/rpc"
	"blossm.local/backend/view"
)

// Transactional fixture exercises the real view owner and transport together.
// Native transaction guarantees are tested by the storage/view owners.
type gatewayBackend struct {
	eventstore.Backend
	mu   sync.Mutex
	rows map[string]eventstore.Projection
}
type gatewayTx struct {
	eventstore.Tx
	rows map[string]eventstore.Projection
}

func (b *gatewayBackend) View(ctx context.Context, fn func(eventstore.Reader) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(&gatewayTx{rows: b.rows})
}
func (b *gatewayBackend) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	rows := map[string]eventstore.Projection{}
	for id, p := range b.rows {
		rows[id] = p
	}
	if err := fn(&gatewayTx{rows: rows}); err != nil {
		return err
	}
	b.rows = rows
	return nil
}
func (tx *gatewayTx) LockRoots(context.Context, []eventstore.Root) error { return nil }
func (tx *gatewayTx) Projection(_ context.Context, id string) (eventstore.Projection, error) {
	p, ok := tx.rows[id]
	if !ok {
		return p, eventstore.ErrNotFound
	}
	return p, nil
}
func (tx *gatewayTx) PutProjection(_ context.Context, p eventstore.Projection) (bool, error) {
	tx.rows[p.ID] = p
	return true, nil
}
func (tx *gatewayTx) Projections(_ context.Context, _, _, _, after string, limit int) ([]eventstore.Projection, error) {
	var ids []string
	for id := range tx.rows {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	var rows []eventstore.Projection
	for _, id := range ids {
		rows = append(rows, tx.rows[id])
	}
	return rows, nil
}
func gatewayScope() view.Scope {
	return view.Scope{Network: "n", Service: "s", Domain: "d", Owner: []eventstore.Predicate{{Path: "owner", Value: json.RawMessage(`"alice"`)}}}
}
func gatewayRoot(id string) eventstore.Root {
	return eventstore.Root{Network: "n", Service: "s", Domain: "d", ID: id}
}
func gatewayAuth(r *http.Request, op, name string) (view.Scope, error) {
	if r.Header.Get("Authorization") != "Bearer fixture" {
		return view.Scope{}, errors.New("secret auth failure")
	}
	if op == "batch" && r.Header.Get("Write") != "yes" {
		return view.Scope{}, dia.ErrForbidden
	}
	return gatewayScope(), nil
}
func gatewayFixture() *gatewayBackend {
	return &gatewayBackend{rows: map[string]eventstore.Projection{
		view.PhysicalID("items", "a"): {ID: view.PhysicalID("items", "a"), Root: gatewayRoot("a"), Version: 1, Body: json.RawMessage(`{"owner":"alice","n":1}`)},
		view.PhysicalID("items", "b"): {ID: view.PhysicalID("items", "b"), Root: gatewayRoot("b"), Version: 1, Body: json.RawMessage(`{"owner":"bob","n":2}`)},
	}}
}
func gatewayRequest(h http.Handler, method, path, body string, auth, write bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if auth {
		r.Header.Set("Authorization", "Bearer fixture")
	}
	if write {
		r.Header.Set("Write", "yes")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestViewGatewayScopesAndStrictInput(t *testing.T) {
	b := gatewayFixture()
	h, err := (ViewGateway{Authorize: gatewayAuth, Stores: map[string]eventstore.Backend{"items": b}}).Handler()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path, body string
		auth, write        bool
		status             int
	}{
		{"GET", "/view-stores/items/a", "", false, false, 401},
		{"GET", "/view-stores/items/a", "", true, false, 200},
		{"GET", "/view-stores/items/b", "", true, false, 404},
		{"GET", "/view-stores/missing/a", "", true, false, 404},
		{"GET", "/view-stores/items/a?owner=bob", "", true, false, 400},
		{"GET", "/view-stores/items?query=%7B%22owner%22:%22bob%22%7D", "", true, false, 400},
		{"GET", "/view-stores/items?query=%7B%22limit%22:-1%7D", "", true, false, 400},
		{"GET", "/view-stores/items?query={}&query={}", "", true, false, 400},
		{"GET", "/view-stores/items?query=%ZZ", "", true, false, 400},
		{"POST", "/view-stores/items/batch", `{"mutations":[]}`, true, false, 403},
		{"POST", "/view-stores/items/batch", `{"mutations":[],"scope":{}}`, true, true, 400},
		{"POST", "/view-stores/items/batch", `{"mutations":[],"mutations":[]}`, true, true, 400},
		{"POST", "/view-stores/items/batch", `{"mutations":[]}`, true, true, 400},
		{"GET", "/view-channels/items", "", true, false, 501},
	}
	for _, tc := range cases {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			w := gatewayRequest(h, tc.method, tc.path, tc.body, tc.auth, tc.write)
			if w.Code != tc.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("error leaked")
			}
		})
	}
	for _, q := range []view.Query{{Limit: 1}, {Predicates: []eventstore.Predicate{{Path: "owner", Value: json.RawMessage(`"bob"`)}}}} {
		raw, _ := json.Marshal(q)
		w := gatewayRequest(h, "GET", "/view-stores/items?"+url.Values{"query": {string(raw)}}.Encode(), "", true, false)
		var p view.Page
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil {
			t.Fatal(w.Code, w.Body.String())
		}
		want := 1
		if len(q.Predicates) > 0 {
			want = 0
		}
		if p.Total != want || len(p.Items) != want {
			t.Fatal(p)
		}
	}
}
func TestViewGatewayBatchRollback(t *testing.T) {
	b := gatewayFixture()
	h, _ := (ViewGateway{Authorize: gatewayAuth, Stores: map[string]eventstore.Backend{"items": b}}).Handler()
	mutate := func(ms []view.Mutation) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(map[string]any{"mutations": ms})
		return gatewayRequest(h, "POST", "/view-stores/items/batch", string(raw), true, true)
	}
	valid := view.Mutation{ID: "a", Root: gatewayRoot("a"), Version: 2, Body: json.RawMessage(`{"owner":"alice","n":3}`)}
	foreign := view.Mutation{ID: "b", Root: gatewayRoot("b"), Version: 2, Body: json.RawMessage(`{"owner":"alice"}`)}
	if w := mutate([]view.Mutation{valid, foreign}); w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	if b.rows[view.PhysicalID("items", "a")].Version != 1 {
		t.Fatal("partial batch committed")
	}
	changedOwner := valid
	changedOwner.Body = json.RawMessage(`{"owner":"bob"}`)
	if w := mutate([]view.Mutation{changedOwner}); w.Code < 400 {
		t.Fatal("owner replacement allowed")
	}
	if b.rows[view.PhysicalID("items", "a")].Version != 1 {
		t.Fatal("owner escaped")
	}
	if w := mutate([]view.Mutation{valid}); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if b.rows[view.PhysicalID("items", "a")].Version != 2 {
		t.Fatal("write not applied")
	}
	w := mutate([]view.Mutation{valid})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"applied":false`) {
		t.Fatal("replay", w.Code, w.Body.String())
	}
}
func TestViewGatewayCompositeAndChannel(t *testing.T) {
	type query struct {
		ID string `json:"id"`
	}
	b := gatewayFixture()
	c := TypedViewComposite(map[string]view.ComponentReader{"items": view.BackendComponent{Backend: b, Name: "items"}}, func(ctx context.Context, q query, scope view.Scope, components *view.Components) (eventstore.Projection, error) {
		scope.Owner[0].Value[1] = 'X' // Must not mutate bound Components authority.
		return components.Get("items", q.ID)
	}, func(ctx context.Context, q query, scope view.Scope, components *view.Components, emit func(eventstore.Projection) error) error {
		p, e := components.Get("items", q.ID)
		if e != nil {
			return e
		}
		return emit(p)
	})
	g := ViewGateway{Authorize: gatewayAuth, Composites: map[string]ViewComposite{"joined": c}, Channel: func(ctx context.Context, s view.Scope, name, key string) (string, error) {
		if string(s.Owner[0].Value) != `"alice"` {
			t.Fatal(s)
		}
		return name + ".alice." + key, nil
	}}
	h, err := g.Handler()
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "/stream"} {
		for _, id := range []string{"a", "b"} {
			raw := `{"id":"` + id + `"}`
			w := gatewayRequest(h, "GET", "/view-composites/joined"+suffix+"?"+url.Values{"query": {raw}}.Encode(), "", true, false)
			want := 200
			if id == "b" {
				want = 404
			}
			if w.Code != want {
				t.Fatal(w.Code, w.Body.String())
			}
			if suffix != "" && id == "a" && w.Header().Get("Content-Type") != "application/x-ndjson" {
				t.Fatal(w.Header())
			}
		}
	}
	w := gatewayRequest(h, "GET", "/view-composites/joined?"+url.Values{"query": {`{"id":"a","owner":"bob"}`}}.Encode(), "", true, false)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
	w = gatewayRequest(h, "GET", "/view-channels/items?key=k", "", true, false)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `"items.alice.k"` {
		t.Fatal(w.Code, w.Body.String())
	}
	w = gatewayRequest(h, "GET", "/view-channels/items?owner=bob", "", true, false)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
func TestViewGatewayStreamFailureAndCancellation(t *testing.T) {
	for _, after := range []bool{false, true} {
		t.Run(map[bool]string{false: "before", true: "after"}[after], func(t *testing.T) {
			h, _ := (ViewGateway{Authorize: gatewayAuth, Composites: map[string]ViewComposite{"broken": {Stream: func(ctx context.Context, s view.Scope, q json.RawMessage, emit func(json.RawMessage) error) error {
				if after {
					if err := emit(json.RawMessage(`{"x":1}`)); err != nil {
						return err
					}
				}
				return errors.New("secret backend failure")
			}}}}).Handler()
			defer func() {
				p := recover()
				if after && p != http.ErrAbortHandler {
					t.Fatalf("partial stream did not abort: %v", p)
				}
				if !after && p != nil {
					t.Fatal(p)
				}
			}()
			w := gatewayRequest(h, "GET", "/view-composites/broken/stream", "", true, false)
			if after {
				t.Fatal("partial success")
			}
			if w.Code != 500 || strings.Contains(w.Body.String(), "secret") {
				t.Fatal(w.Code, w.Body.String())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	var saved *view.Components
	c := TypedViewComposite[struct{}, int](nil, func(ctx context.Context, q struct{}, s view.Scope, components *view.Components) (int, error) {
		saved = components
		cancel()
		return 1, nil
	}, nil)
	if _, err := c.Read(ctx, gatewayScope(), nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := saved.Get("none", "a"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := (ViewGateway{}).Handler(); err == nil {
		t.Fatal("missing auth")
	}
	h, _ := (ViewGateway{Authorize: func(*http.Request, string, string) (view.Scope, error) { return view.Scope{}, nil }}).Handler()
	if w := gatewayRequest(h, "GET", "/view-channels/a", "", true, false); w.Code != 500 {
		t.Fatal("empty scope accepted", w.Code)
	}
}

// In-process HTTP transport runs the actual RPC serialization and gateway
// handler, with no sockets or ambient authentication.
type gatewayRoundTrip struct{ handler http.Handler }

func (rt gatewayRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	rt.handler.ServeHTTP(w, r)
	return w.Result(), nil
}
func TestViewGatewayRPC(t *testing.T) {
	b := gatewayFixture()
	h, _ := (ViewGateway{Authorize: gatewayAuth, Stores: map[string]eventstore.Backend{"items": b}, Composites: map[string]ViewComposite{"echo": TypedViewComposite[struct{}, []int](nil, func(context.Context, struct{}, view.Scope, *view.Components) ([]int, error) { return []int{7}, nil }, func(ctx context.Context, _ struct{}, _ view.Scope, _ *view.Components, emit func([]int) error) error {
		return emit([]int{8})
	})}, Channel: func(context.Context, view.Scope, string, string) (string, error) { return "channel", nil }}).Handler()
	rc, err := rpc.New("http://fixture.invalid", rpc.Options{HTTP: &http.Client{Transport: gatewayRoundTrip{h}}})
	if err != nil {
		t.Fatal(err)
	}
	c := ViewClient{RPC: rc}
	ctx := context.Background()
	headers := http.Header{"Authorization": {"Bearer fixture"}, "Write": {"yes"}}
	p, err := c.Get(ctx, "items", "a", headers)
	if err != nil || p.ID != "a" {
		t.Fatal(p, err)
	}
	page, err := c.List(ctx, "items", view.Query{}, headers)
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	out, err := c.Batch(ctx, "items", []view.Mutation{{ID: "a", Root: gatewayRoot("a"), Version: 2, Body: json.RawMessage(`{"owner":"alice"}`)}}, headers)
	if err != nil || len(out) != 1 || !out[0].Applied {
		t.Fatal(out, err)
	}
	var number []int
	if err := c.ReadComposite(ctx, "echo", struct{}{}, headers, &number); err != nil || len(number) != 1 || number[0] != 7 {
		t.Fatal(number, err)
	}
	count := 0
	if err := c.StreamComposite(ctx, "echo", struct{}{}, headers, func(ctx context.Context, raw json.RawMessage) error {
		count++
		if string(raw) != "[8]" {
			t.Fatal(string(raw))
		}
		return nil
	}); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	channel, err := c.Channel(ctx, "items", "k", headers)
	if err != nil || channel != "channel" {
		t.Fatal(channel, err)
	}
	if _, err = c.Get(ctx, "items", "a", nil); err == nil {
		t.Fatal("unauthenticated RPC")
	}
	for _, name := range []string{"", "..", "a/b", "%2e%2e"} {
		if _, err = c.Get(ctx, name, "a", headers); err == nil {
			t.Fatal("invalid path", name)
		}
	}
	if _, err = (ViewClient{}).List(ctx, "items", view.Query{}, headers); err == nil {
		t.Fatal("nil RPC")
	}
}

func TestViewGatewayStreamWireAbort(t *testing.T) {
	h, _ := (ViewGateway{Authorize: gatewayAuth, Composites: map[string]ViewComposite{"broken": {Stream: func(ctx context.Context, s view.Scope, q json.RawMessage, emit func(json.RawMessage) error) error {
		if err := emit(json.RawMessage(`{"x":1}`)); err != nil {
			return err
		}
		return errors.New("failed")
	}}}}).Handler()
	server := httptest.NewServer(h)
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/view-composites/broken/stream", nil)
	req.Header.Set("Authorization", "Bearer fixture")
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err == nil || string(data) != "{\"x\":1}\n" {
		t.Fatalf("partial wire result %q %v", data, err)
	}
}

func TestViewGatewayStreamUsesSharedContract(t *testing.T) {
	var late func(json.RawMessage) error
	h, _ := (ViewGateway{Authorize: gatewayAuth, Composites: map[string]ViewComposite{
		"invalid": {Stream: func(ctx context.Context, s view.Scope, q json.RawMessage, emit func(json.RawMessage) error) error {
			late = emit
			_ = emit(json.RawMessage(`8`)) // Ignoring producer errors cannot make success.
			_ = emit(json.RawMessage(`{"accepted":false}`))
			return nil
		}},
		"empty": {Stream: func(context.Context, view.Scope, json.RawMessage, func(json.RawMessage) error) error { return nil }},
	}}).Handler()
	w := gatewayRequest(h, "GET", "/view-composites/invalid/stream", "", true, false)
	if w.Code != 500 || strings.Contains(w.Body.String(), "accepted") {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := late(json.RawMessage(`{}`)); !errors.Is(err, fact.ErrStreamClosed) {
		t.Fatal("late emitter still active", err)
	}
	w = gatewayRequest(h, "GET", "/view-composites/empty/stream", "", true, false)
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatal(w.Code, w.Body.String(), w.Header())
	}
}
