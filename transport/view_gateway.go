package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/url"
	"strings"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/fact"
	"blossm.local/backend/rpc"
	"blossm.local/backend/view"
)

// ViewComposite callbacks are trusted application code. They must use the
// supplied scope for every read. Stream records must be JSON objects or arrays. TypedViewComposite binds component access using
// view.ExecuteComposite. Stream is synchronous and must honor context/emit errors.
type ViewComposite struct {
	Read   func(context.Context, view.Scope, json.RawMessage) (any, error)
	Stream func(context.Context, view.Scope, json.RawMessage, func(json.RawMessage) error) error
}

// TypedViewComposite decodes a typed query strictly and binds component readers
// to the authenticated scope. A streaming main emits typed records synchronously.
func TypedViewComposite[Q, R any](readers map[string]view.ComponentReader, read view.CompositeMain[Q, R], stream func(context.Context, Q, view.Scope, *view.Components, func(R) error) error) ViewComposite {
	readersCopy := make(map[string]view.ComponentReader, len(readers))
	for k, v := range readers {
		readersCopy[k] = v
	}
	var out ViewComposite
	parse := func(raw json.RawMessage) (Q, error) {
		var q Q
		if len(raw) == 0 {
			raw = json.RawMessage(`{}`)
		}
		if err := eventstore.Decode(raw, &q); err != nil {
			return q, dia.ErrInvalid
		}
		return q, nil
	}
	if read != nil {
		out.Read = func(ctx context.Context, scope view.Scope, raw json.RawMessage) (any, error) {
			q, err := parse(raw)
			if err != nil {
				return nil, err
			}
			return view.ExecuteComposite(ctx, scope, q, readersCopy, read)
		}
	}
	if stream != nil {
		out.Stream = func(ctx context.Context, scope view.Scope, raw json.RawMessage, emit func(json.RawMessage) error) error {
			q, err := parse(raw)
			if err != nil {
				return err
			}
			_, err = view.ExecuteComposite(ctx, scope, q, readersCopy, func(ctx context.Context, q Q, scope view.Scope, c *view.Components) (struct{}, error) {
				err := stream(ctx, q, scope, c, func(record R) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					b, err := eventstore.Encode(record)
					if err != nil {
						return err
					}
					return emit(b)
				})
				return struct{}{}, err
			})
			return err
		}
	}
	return out
}

// ViewGateway is independently mountable. Authorize must verify identity and
// permission for the exact operation/name, especially batch writes. It alone
// selects source and owner authority; request parameters never supply scope.
// Configure registries before Handler; later map changes do not alter routes.
type ViewCSV struct {
	Columns []view.Column
	Format  func(eventstore.Projection) (json.RawMessage, error)
}

type ViewGateway struct {
	Bootstrap map[string]func(context.Context, view.Scope) (string, error)
	Empty     map[string]func(context.Context, view.Scope, view.Query) (json.RawMessage, error)
	Updates   map[string]func(context.Context, view.Scope, view.Query) (string, error)
	CSV       map[string]ViewCSV

	Authorize  func(*http.Request, string, string) (view.Scope, error)
	Stores     map[string]eventstore.Backend
	Composites map[string]ViewComposite
	// Channel resolves a scoped channel identifier. It does not subscribe or push.
	Channel func(context.Context, view.Scope, string, string) (string, error)
}

func (g ViewGateway) Handler() (http.Handler, error) {
	if g.Authorize == nil {
		return nil, errors.New("view authenticator required")
	}
	g.Bootstrap = maps.Clone(g.Bootstrap)
	g.Empty = maps.Clone(g.Empty)
	g.Updates = maps.Clone(g.Updates)
	g.CSV = maps.Clone(g.CSV)
	for name, csv := range g.CSV {
		csv.Columns = append([]view.Column(nil), csv.Columns...)
		g.CSV[name] = csv
	}
	stores := make(map[string]eventstore.Backend, len(g.Stores))
	for name, backend := range g.Stores {
		if name == "" || backend == nil {
			return nil, errors.New("invalid view store registration")
		}
		stores[name] = backend
	}
	composites := make(map[string]ViewComposite, len(g.Composites))
	for name, c := range g.Composites {
		if name == "" || (c.Read == nil && c.Stream == nil) {
			return nil, errors.New("invalid view composite registration")
		}
		composites[name] = c
	}
	mux := http.NewServeMux()
	auth := func(operation string, fn func(http.ResponseWriter, *http.Request, view.Scope) error) http.HandlerFunc {
		return wrap(func(w http.ResponseWriter, r *http.Request) error {
			scope, err := g.Authorize(r, operation, r.PathValue("name"))
			if err != nil {
				if errors.Is(err, dia.ErrForbidden) {
					return dia.ErrForbidden
				}
				return dia.ErrUnauthorized
			}
			// Validate and detach scope using the same owner as composite execution.
			scope, err = view.ExecuteComposite(r.Context(), scope, struct{}{}, nil, func(_ context.Context, _ struct{}, s view.Scope, _ *view.Components) (view.Scope, error) {
				return s, nil
			})
			if err != nil {
				return err
			}
			err = fn(w, r, scope)
			if errors.Is(err, eventstore.ErrNotFound) {
				return write(w, 404, map[string]string{"error": "not found"})
			}
			return err
		})
	}
	store := func(r *http.Request, scope view.Scope) (*view.Store, error) {
		b, ok := stores[r.PathValue("name")]
		if !ok {
			return nil, eventstore.ErrNotFound
		}
		return &view.Store{Backend: b, Scope: scope, Name: r.PathValue("name")}, nil
	}
	mux.HandleFunc("GET /view-bootstrap/{name}", auth("bootstrap", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		if r.URL.RawQuery != "" {
			return dia.ErrInvalid
		}
		hook := g.Bootstrap[r.PathValue("name")]
		if hook == nil {
			return eventstore.ErrNotFound
		}
		id, err := view.ExecuteComposite(r.Context(), scope, struct{}{}, nil, func(ctx context.Context, _ struct{}, detached view.Scope, _ *view.Components) (string, error) {
			return hook(ctx, detached)
		})
		if err != nil {
			return err
		}
		s, err := store(r, scope)
		if err != nil {
			return err
		}
		p, err := s.Get(r.Context(), id)
		if err != nil {
			return err
		}
		return write(w, 200, p)
	}))
	mux.HandleFunc("GET /view-stores/{name}/{id}", auth("read", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		if r.URL.RawQuery != "" {
			return dia.ErrInvalid
		}
		s, err := store(r, scope)
		if err != nil {
			return err
		}
		p, err := s.Get(r.Context(), r.PathValue("id"))
		if err != nil {
			return err
		}
		return write(w, 200, p)
	}))
	mux.HandleFunc("GET /view-stores/{name}", auth("list", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		raw, err := viewQuery(r, "query", "download")
		if err != nil {
			return err
		}
		var q view.Query
		if len(raw) != 0 {
			if err := eventstore.Decode(raw, &q); err != nil {
				return dia.ErrInvalid
			}
		}
		if q.Offset < 0 || q.Limit < 0 || q.Limit > view.MaxLimit {
			return dia.ErrInvalid
		}
		s, err := store(r, scope)
		if err != nil {
			return err
		}
		page, err := s.List(r.Context(), q)
		if err != nil {
			return err
		}
		return g.writeViewPage(w, r, scope, q, page)
	}))
	mux.HandleFunc("POST /view-stores/{name}/batch", auth("batch", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		if r.URL.RawQuery != "" {
			return dia.ErrInvalid
		}
		var in struct {
			Mutations []view.Mutation `json:"mutations"`
		}
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if len(in.Mutations) == 0 || len(in.Mutations) > view.MaxLimit {
			return dia.ErrInvalid
		}
		s, err := store(r, scope)
		if err != nil {
			return err
		}
		out, err := s.Batch(r.Context(), in.Mutations)
		if err != nil {
			return err
		}
		return write(w, 200, out)
	}))
	mux.HandleFunc("GET /view-composites/{name}", auth("composite", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		raw, err := viewQuery(r, "query")
		if err != nil {
			return err
		}
		c, ok := composites[r.PathValue("name")]
		if !ok {
			return eventstore.ErrNotFound
		}
		if c.Read == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return nil
		}
		out, err := c.Read(r.Context(), scope, raw)
		if err != nil {
			return err
		}
		return write(w, 200, out)
	}))
	mux.HandleFunc("GET /view-composites/{name}/stream", auth("stream", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		raw, err := viewQuery(r, "query")
		if err != nil {
			return err
		}
		c, ok := composites[r.PathValue("name")]
		if !ok {
			return eventstore.ErrNotFound
		}
		if c.Stream == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return nil
		}
		wrote := false
		producer := fact.Procedure{StreamFn: func(ctx context.Context, in fact.Request, yield fact.Yield) error {
			return c.Stream(ctx, scope, in.Query, func(record json.RawMessage) error { return yield(ctx, record) })
		}}
		err = producer.Stream(r.Context(), fact.Request{Query: raw}, func(ctx context.Context, record json.RawMessage) error {
			if err := r.Context().Err(); err != nil {
				return err
			}
			b, err := eventstore.Encode(record)
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			wrote = true
			if _, err := w.Write(append(b, '\n')); err != nil {
				return err
			}
			return http.NewResponseController(w).Flush()
		})
		if err == nil {
			err = r.Context().Err()
		}
		if err != nil && wrote {
			panic(http.ErrAbortHandler)
		}
		if err == nil && !wrote {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(200)
		}
		return err
	}))
	mux.HandleFunc("GET /view-channels/{name}", auth("channel", func(w http.ResponseWriter, r *http.Request, scope view.Scope) error {
		key, err := viewQuery(r, "key")
		if err != nil {
			return err
		}
		if g.Channel == nil {
			w.WriteHeader(http.StatusNotImplemented)
			return nil
		}
		channel, err := g.Channel(r.Context(), scope, r.PathValue("name"), string(key))
		if err != nil {
			return err
		}
		if channel == "" {
			return errors.New("empty view channel")
		}
		return write(w, 200, channel)
	}))
	return mux, nil
}

// Query selection is bounded and unambiguous. No context, claims, owner or
// bootstrap query fields are interpreted as authority.
func viewQuery(r *http.Request, field string, extras ...string) (json.RawMessage, error) {
	if len(r.URL.RawQuery) > 64<<10 {
		return nil, dia.ErrInvalid
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, dia.ErrInvalid
	}
	for k, v := range values {
		allowed := k == field
		for _, extra := range extras {
			allowed = allowed || k == extra
		}
		if !allowed || len(v) != 1 {
			return nil, dia.ErrInvalid
		}
	}
	raw := json.RawMessage(values.Get(field))
	if field == "query" && len(raw) != 0 {
		canonical, err := eventstore.Canonical(raw)
		if err != nil {
			return nil, dia.ErrInvalid
		}
		return canonical, nil
	}
	return raw, nil
}

// Presentation runs only after scoped reads; hooks receive detached authority.
func (g ViewGateway) writeViewPage(w http.ResponseWriter, r *http.Request, scope view.Scope, q view.Query, page view.Page) error {
	name := r.PathValue("name")
	if download := r.URL.Query().Get("download"); download != "" {
		if download != "csv" {
			return dia.ErrInvalid
		}
		config, ok := g.CSV[name]
		if !ok {
			return dia.ErrUnavailable
		}
		docs := make([]view.Document, len(page.Items))
		for i, p := range page.Items {
			if err := r.Context().Err(); err != nil {
				return err
			}
			d, err := view.Format(p, config.Format)
			if err != nil {
				return err
			}
			docs[i] = d
		}
		var buf bytes.Buffer
		if err := view.CSV(&buf, docs, config.Columns); err != nil {
			return err
		}
		if err := r.Context().Err(); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=views.csv")
		_, err := w.Write(buf.Bytes())
		return err
	}
	if page.NextOffset != nil {
		next := q
		next.Offset = *page.NextOffset
		encoded, err := eventstore.Encode(next)
		if err != nil {
			return err
		}
		page.Next = r.URL.EscapedPath() + "?" + url.Values{"query": {string(encoded)}}.Encode()
	}
	if hook := g.Empty[name]; hook != nil && len(page.Items) == 0 {
		empty, err := view.ExecuteComposite(r.Context(), scope, q, nil, func(ctx context.Context, q view.Query, s view.Scope, _ *view.Components) (json.RawMessage, error) {
			return hook(ctx, s, q)
		})
		if err != nil {
			return err
		}
		page.Empty, err = eventstore.Canonical(empty)
		if err != nil {
			return err
		}
	}
	if hook := g.Updates[name]; hook != nil {
		link, err := view.ExecuteComposite(r.Context(), scope, q, nil, func(ctx context.Context, q view.Query, s view.Scope, _ *view.Components) (string, error) {
			return hook(ctx, s, q)
		})
		if err != nil {
			return err
		}
		page.Updates = link
	}
	return write(w, 200, page)
}

// ViewClient reuses rpc.Client's deadlines, bounded bodies and no-retry policy.
// Headers carry credentials supplied by application composition, never scope.
type ViewClient struct{ RPC *rpc.Client }

func viewPath(prefix, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\%\x00\r\n") {
		return "", dia.ErrInvalid
	}
	return prefix + url.PathEscape(name), nil
}
func (c ViewClient) call(ctx context.Context, method, path string, query url.Values, body any, headers http.Header, out any) error {
	if c.RPC == nil {
		return errors.New("view RPC client required")
	}
	response, err := c.RPC.Do(ctx, method, path, query, body, headers)
	if err != nil {
		return err
	}
	return eventstore.Decode(response.Body, out)
}
func (c ViewClient) Get(ctx context.Context, name, id string, headers http.Header) (eventstore.Projection, error) {
	var out eventstore.Projection
	path, err := viewPath("/view-stores/", name)
	if err != nil {
		return out, err
	}
	path, err = viewPath(path+"/", id)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, "GET", path, nil, nil, headers, &out)
	return out, err
}
func (c ViewClient) List(ctx context.Context, name string, query view.Query, headers http.Header) (view.Page, error) {
	var out view.Page
	path, err := viewPath("/view-stores/", name)
	if err != nil {
		return out, err
	}
	raw, err := eventstore.Encode(query)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, "GET", path, url.Values{"query": {string(raw)}}, nil, headers, &out)
	return out, err
}
func (c ViewClient) Batch(ctx context.Context, name string, mutations []view.Mutation, headers http.Header) ([]view.Result, error) {
	var out []view.Result
	path, err := viewPath("/view-stores/", name)
	if err != nil {
		return nil, err
	}
	err = c.call(ctx, "POST", path+"/batch", nil, struct {
		Mutations []view.Mutation `json:"mutations"`
	}{mutations}, headers, &out)
	return out, err
}
func (c ViewClient) ReadComposite(ctx context.Context, name string, query any, headers http.Header, out any) error {
	path, err := viewPath("/view-composites/", name)
	if err != nil {
		return err
	}
	raw, err := eventstore.Encode(query)
	if err != nil {
		return err
	}
	return c.call(ctx, "GET", path, url.Values{"query": {string(raw)}}, nil, headers, out)
}
func (c ViewClient) StreamComposite(ctx context.Context, name string, query any, headers http.Header, yield fact.Yield) error {
	path, err := viewPath("/view-composites/", name)
	if err != nil {
		return err
	}
	raw, err := eventstore.Encode(query)
	if err != nil {
		return err
	}
	if c.RPC == nil {
		return errors.New("view RPC client required")
	}
	return (fact.Client{RPC: c.RPC}).Stream(ctx, path+"/stream", url.Values{"query": {string(raw)}}, headers, yield)
}
func (c ViewClient) Channel(ctx context.Context, name, key string, headers http.Header) (string, error) {
	var out string
	path, err := viewPath("/view-channels/", name)
	if err != nil {
		return out, err
	}
	err = c.call(ctx, "GET", path, url.Values{"key": {key}}, nil, headers, &out)
	return out, err
}
