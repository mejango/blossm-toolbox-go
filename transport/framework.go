package transport

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/fact"
	"github.com/mejango/blossm-toolbox-go/procedures/job"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

type Identity struct {
	Context command.Context
	Claims  json.RawMessage
}

// Framework is an authenticated internal procedure gateway. Authorize must
// verify the service identity/audience and route authority. No request-body
// claims or context are accepted as authenticated authority.
type Framework struct {
	Authorize        func(*http.Request) (Identity, error)
	Views            *ViewGateway
	Projections      *ProjectionRoutes
	Commands         map[string]command.Procedure
	Facts            map[string]fact.Procedure
	Jobs             map[string]job.Procedure
	Store            *eventstore.Store
	BlockSigner      eventstore.Signer
	Network          string
	Publisher        eventstore.Publisher
	PublicationBatch int
	RecoveryError    func(error)
}

func (f Framework) recover(ctx context.Context) {
	if f.Store == nil || f.Store.Local {
		return
	}
	limit := f.PublicationBatch
	if limit == 0 {
		limit = 100
	}
	attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if e := f.Store.Deliver(attempt, f.Publisher, limit); e != nil {
		if f.RecoveryError != nil {
			f.RecoveryError(e)
		} else {
			slog.Error("publication recovery failed", "pending", true)
		}
	}
}

// Handler enforces required durability composition before accepting requests.
func (f Framework) Handler(ctx context.Context) (http.Handler, error) {
	if f.Authorize == nil {
		return nil, errors.New("service authenticator required")
	}
	if f.Store != nil {
		if f.Network == "" {
			return nil, errors.New("store network required")
		}
		if !f.Store.Local && f.Publisher == nil {
			return nil, errors.New("remote durable publication recovery required")
		}
	}
	if f.PublicationBatch < 0 || f.PublicationBatch > 10000 {
		return nil, errors.New("invalid publication batch")
	}
	mux := http.NewServeMux()
	if f.Views != nil {
		handler, err := f.Views.Handler()
		if err != nil {
			return nil, err
		}
		for _, prefix := range []string{"/view-stores/", "/view-composites/", "/view-channels/", "/view-bootstrap/"} {
			mux.Handle(prefix, handler)
		}
	}
	if f.Projections != nil {
		handler, err := f.Projections.Handler()
		if err != nil {
			return nil, err
		}
		mux.Handle("/projections/", handler)
	}
	authenticated := func(fn func(http.ResponseWriter, *http.Request, Identity) error) http.HandlerFunc {
		return Wrap(func(w http.ResponseWriter, r *http.Request) error {
			identity, e := f.Authorize(r)
			if e != nil {
				w.WriteHeader(http.StatusUnauthorized)
				return nil
			}
			return fn(w, r, identity)
		})
	}
	mux.HandleFunc("POST /commands/{name}", authenticated(func(w http.ResponseWriter, r *http.Request, identity Identity) error {
		p, ok := f.Commands[r.PathValue("name")]
		if !ok {
			w.WriteHeader(404)
			return nil
		}
		var in command.Request
		if e := Decode(w, r, &in); e != nil {
			return e
		}
		in.Context = identity.Context
		in.Claims = identity.Claims
		in.Transaction.Claims = identity.Claims
		out, e := p.Execute(r.Context(), in)
		if e != nil {
			var callback *command.CallbackError
			if !errors.As(e, &callback) {
				return e
			}
			w.Header().Set("Blossm-Callback-Status", "failed")
		}
		f.recover(r.Context())
		for k, v := range out.Headers {
			w.Header().Set(k, v)
		}
		status := out.StatusCode
		if status == 0 {
			status = 200
		}
		return Write(w, status, out)
	}))
	mux.HandleFunc("GET /facts/{name}", authenticated(func(w http.ResponseWriter, r *http.Request, identity Identity) error {
		p, ok := f.Facts[r.PathValue("name")]
		if !ok {
			w.WriteHeader(404)
			return nil
		}
		raw := json.RawMessage(r.URL.Query().Get("query"))
		in := fact.Request{Query: raw, Root: r.URL.Query().Get("root"), Context: identity.Context, Claims: identity.Claims}
		if r.URL.Query().Get("stream") == "true" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			w.Header().Set("Cache-Control", "no-store")
			wrote := false
			e := p.Stream(r.Context(), in, func(ctx context.Context, record json.RawMessage) error {
				if e := ctx.Err(); e != nil {
					return e
				}
				wrote = true
				if e := json.NewEncoder(w).Encode(record); e != nil {
					return e
				}
				return http.NewResponseController(w).Flush()
			})
			if e != nil && wrote {
				panic(http.ErrAbortHandler)
			}
			return e
		}
		out, e := p.Read(r.Context(), in)
		if e != nil {
			return e
		}
		return Write(w, 200, out)
	}))
	mux.HandleFunc("POST /jobs/{name}", authenticated(func(w http.ResponseWriter, r *http.Request, identity Identity) error {
		p, ok := f.Jobs[r.PathValue("name")]
		if !ok {
			w.WriteHeader(404)
			return nil
		}
		var in job.Input
		if e := Decode(w, r, &in); e != nil {
			return e
		}
		in.Context, _ = eventstore.Encode(identity.Context)
		in.Claims = append(json.RawMessage(nil), identity.Claims...)
		if e := p.Execute(r.Context(), in); e != nil {
			return e
		}
		f.recover(r.Context())
		w.WriteHeader(204)
		return nil
	}))
	if f.Store != nil {
		f.storeRoutes(mux, authenticated)
		mux.HandleFunc("POST /events", authenticated(func(w http.ResponseWriter, r *http.Request, identity Identity) error {
			var in struct {
				Events []eventstore.Input `json:"events"`
			}
			if e := Decode(w, r, &in); e != nil {
				return e
			}
			trustedContext, e := eventstore.Encode(identity.Context)
			if e != nil {
				return e
			}
			for i := range in.Events {
				if in.Events[i].Root.Network != f.Network {
					return eventstore.ErrInvalid
				}
				in.Events[i].Transaction.Claims = identity.Claims
				in.Events[i].Context = trustedContext
			}
			out, e := f.Store.Append(r.Context(), in.Events)
			if e != nil {
				return e
			}
			f.recover(r.Context())
			return Write(w, 200, out)
		}))
		mux.HandleFunc("POST /aggregate", authenticated(func(w http.ResponseWriter, r *http.Request, identity Identity) error {
			var root eventstore.Root
			if e := Decode(w, r, &root); e != nil {
				return e
			}
			if root.Network != f.Network {
				return eventstore.ErrInvalid
			}
			out, e := f.Store.Aggregate(r.Context(), root)
			if e != nil {
				return e
			}
			return Write(w, 200, out)
		}))
		mux.HandleFunc("POST /query", authenticated(func(w http.ResponseWriter, r *http.Request, identity Identity) error {
			var in struct {
				Service    string                 `json:"service"`
				Domain     string                 `json:"domain"`
				Predicates []eventstore.Predicate `json:"predicates"`
			}
			if e := Decode(w, r, &in); e != nil {
				return e
			}
			out, e := f.Store.Query(r.Context(), f.Network, in.Service, in.Domain, in.Predicates)
			if e != nil {
				return e
			}
			return Write(w, 200, out)
		}))
	}
	f.recover(ctx)
	return mux, nil
}
