// Package transport adapts typed Dia operations to ordinary HTTP containers.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/fact"
	"github.com/mejango/blossm-toolbox-go/view"
)

type API struct {
	Dia   *dia.Service
	Store *eventstore.Store
}
type endpoint func(http.ResponseWriter, *http.Request) error

func token(r *http.Request) string {
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return dia.ErrInvalid
	}
	if err = eventstore.Decode(raw, v); err != nil {
		return dia.ErrInvalid
	}
	return nil
}

func write(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Cache-Control", "no-store")
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return nil
	}
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(append(body, '\n'))
	return err
}
func wrap(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if e := fn(w, r.WithContext(ctx)); e != nil {
			status := http.StatusInternalServerError
			message := "internal error"
			switch {
			case errors.Is(e, dia.ErrUnauthorized):
				status = 401
				message = "unauthorized"
			case errors.Is(e, eventstore.ErrNotFound):
				status = 404
				message = "not found"
			case errors.Is(e, dia.ErrUnavailable):
				status = 503
				message = "required service unavailable"
			case errors.Is(e, dia.ErrForbidden), errors.Is(e, command.ErrForbidden):
				status = 403
				message = "forbidden"
			case errors.Is(e, dia.ErrInvalid), errors.Is(e, eventstore.ErrInvalid), errors.Is(e, command.ErrInvalid), errors.Is(e, fact.ErrInvalid), errors.Is(e, view.ErrInvalid):
				status = 400
				message = "invalid input"
			case errors.Is(e, eventstore.ErrConflict), errors.Is(e, eventstore.ErrIdempotency):
				status = 409
				message = "conflict"
			case errors.Is(e, command.ErrQueueUncertain):
				status = 503
				message = "queue acceptance uncertain; retry identical request"
			case errors.Is(e, eventstore.ErrUncertain):
				status = 503
				message = "commit uncertain; retry identical request"
			case errors.Is(e, context.DeadlineExceeded):
				status = 504
				message = "deadline exceeded"
			}
			body := map[string]any{"error": message}
			var partial interface{ PartialResult() any }
			if errors.As(e, &partial) {
				body["result"] = partial.PartialResult()
			}
			_ = write(w, status, body)
		}
	}
}
func (a API) Handler() http.Handler {
	mux := http.NewServeMux()
	a.commands(mux)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _ = write(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /commands/session/start", wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in struct {
			Device dia.Device `json:"device"`
		}
		if e := decode(w, r, &in); e != nil {
			return e
		}
		ip, _, e := net.SplitHostPort(r.RemoteAddr)
		if e == nil {
			in.Device.IP = ip
		}
		out, e := a.Dia.StartSession(r.Context(), r.Header.Get("Idempotency-Key"), in.Device)
		if e != nil {
			return e
		}
		return write(w, 201, out)
	}))
	mux.HandleFunc("POST /commands/session/terminate", wrap(func(w http.ResponseWriter, r *http.Request) error {
		if e := a.Dia.TerminateSession(r.Context(), token(r), r.Header.Get("Idempotency-Key")); e != nil {
			return e
		}
		return write(w, 200, map[string]bool{"completed": true})
	}))
	mux.HandleFunc("GET /facts/public-key", wrap(func(w http.ResponseWriter, r *http.Request) error {
		key, e := a.Dia.PublicKey()
		if e != nil {
			return e
		}
		return write(w, 200, map[string]string{"algorithm": "ES256", "pem": string(key)})
	}))
	mux.HandleFunc("GET /views/session", wrap(func(w http.ResponseWriter, r *http.Request) error {
		c, e := a.Dia.AuthenticateAccess(r.Context(), token(r))
		if e != nil {
			return e
		}
		var p eventstore.Projection
		e = a.Store.Backend.View(r.Context(), func(reader eventstore.Reader) error {
			var e error
			p, e = reader.Projection(r.Context(), c.Context.Session.Key())
			return e
		})
		if e != nil {
			return e
		}
		if p.Deleted {
			return dia.ErrForbidden
		}
		return write(w, 200, p)
	}))
	return mux
}

// SessionProjection replaces a single session row with the authoritative
// aggregate and revision. Duplicate/out-of-order notices cannot roll it back.
type SessionProjection struct{ Store *eventstore.Store }

func (p SessionProjection) Publish(ctx context.Context, notice eventstore.Publication) error {
	root := notice.Event.Input.Root
	if root.Service != "core" || root.Domain != "session" {
		return nil
	}
	a, e := p.Store.Aggregate(ctx, root)
	if e != nil {
		return e
	}
	var state dia.SessionState
	if e = json.Unmarshal(a.State, &state); e != nil {
		return e
	}
	body, e := eventstore.Encode(struct {
		Device dia.Device `json:"device"`
	}{state.Device})
	if e != nil {
		return e
	}
	_, e = p.Store.Project(ctx, eventstore.Projection{ID: root.Key(), Root: root, Version: a.Version, Body: body, Deleted: state.Terminated != ""})
	return e
}
