package transport

import (
	"blossm.local/backend/eventstore"
	"encoding/json"
	"net/http"
	"time"
)

// ndjson aborts partial output on failure so clients cannot mistake truncation
// for a successful, empty remainder. A failure before output uses normal errors.
func ndjson(w http.ResponseWriter, produce func(func(any) error) error) error {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	wrote := false
	err := produce(func(value any) error {
		wrote = true
		if err := json.NewEncoder(w).Encode(value); err != nil {
			return err
		}
		return http.NewResponseController(w).Flush()
	})
	if err != nil && wrote {
		panic(http.ErrAbortHandler)
	}
	return err
}

func (f Framework) storeRoutes(mux *http.ServeMux, authenticated func(func(http.ResponseWriter, *http.Request, Identity) error) http.HandlerFunc) {
	if f.BlockSigner != nil {
		mux.HandleFunc("POST /blocks", authenticated(func(w http.ResponseWriter, r *http.Request, _ Identity) error {
			var in struct {
				MaxRoots int `json:"maxRoots"`
			}
			if err := decode(w, r, &in); err != nil {
				return err
			}
			if in.MaxRoots < 1 || in.MaxRoots > 10000 {
				return eventstore.ErrInvalid
			}
			block, err := f.Store.BuildBlock(r.Context(), in.MaxRoots, f.BlockSigner)
			if err != nil {
				return err
			}
			f.recover(r.Context())
			return write(w, 200, block)
		}))
	}
	mux.HandleFunc("POST /count", authenticated(func(w http.ResponseWriter, r *http.Request, _ Identity) error {
		var root eventstore.Root
		if err := decode(w, r, &root); err != nil {
			return err
		}
		if root.Network != f.Network {
			return eventstore.ErrInvalid
		}
		count, err := f.Store.Count(r.Context(), root)
		if err != nil {
			return err
		}
		return write(w, 200, struct {
			Count int64 `json:"count"`
		}{count})
	}))
	mux.HandleFunc("POST /history", authenticated(func(w http.ResponseWriter, r *http.Request, _ Identity) error {
		var in struct {
			Root           eventstore.Root `json:"root"`
			ThroughNumber  *int64          `json:"throughNumber"`
			ThroughCreated *time.Time      `json:"throughCreated"`
			IncludeEvents  bool            `json:"includeEvents"`
		}
		if err := decode(w, r, &in); err != nil {
			return err
		}
		if in.Root.Network != f.Network {
			return eventstore.ErrInvalid
		}
		out, err := f.Store.History(r.Context(), in.Root, eventstore.HistoryOptions{ThroughNumber: in.ThroughNumber, ThroughCreated: in.ThroughCreated, IncludeEvents: in.IncludeEvents})
		if err != nil {
			return err
		}
		return write(w, 200, out)
	}))
	mux.HandleFunc("GET /roots", authenticated(func(w http.ResponseWriter, r *http.Request, _ Identity) error {
		return ndjson(w, func(yield func(any) error) error {
			return f.Store.StreamRoots(r.Context(), f.Network, r.URL.Query().Get("service"), r.URL.Query().Get("domain"), func(root eventstore.Root) error { return yield(root) })
		})
	}))
	mux.HandleFunc("GET /aggregates", authenticated(func(w http.ResponseWriter, r *http.Request, _ Identity) error {
		return ndjson(w, func(yield func(any) error) error {
			return f.Store.StreamAggregates(r.Context(), f.Network, r.URL.Query().Get("service"), r.URL.Query().Get("domain"), r.URL.Query().Get("root"), func(aggregate eventstore.Aggregate) error { return yield(aggregate) })
		})
	}))
}
