// Package transport adapts framework procedures to ordinary HTTP.
package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/mejango/blossm-toolbox-go/lib/authentication_error"
	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/fact"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
)

type endpoint func(http.ResponseWriter, *http.Request) error

func Decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return authn.ErrInvalid
	}
	if err = canonicaljson.Decode(raw, v); err != nil {
		return authn.ErrInvalid
	}
	return nil
}

func Write(w http.ResponseWriter, status int, v any) error {
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
func Wrap(fn endpoint) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if e := fn(w, r.WithContext(ctx)); e != nil {
			status := http.StatusInternalServerError
			message := "internal error"
			switch {
			case errors.Is(e, authn.ErrUnauthorized):
				status = 401
				message = "unauthorized"
			case errors.Is(e, eventlib.ErrNotFound):
				status = 404
				message = "not found"
			case errors.Is(e, authn.ErrUnavailable):
				status = 503
				message = "required service unavailable"
			case errors.Is(e, authn.ErrForbidden), errors.Is(e, command.ErrForbidden):
				status = 403
				message = "forbidden"
			case errors.Is(e, authn.ErrInvalid), errors.Is(e, eventlib.ErrInvalid), errors.Is(e, command.ErrInvalid), errors.Is(e, fact.ErrInvalid), errors.Is(e, view.ErrInvalid):
				status = 400
				message = "invalid input"
			case errors.Is(e, eventlib.ErrConflict), errors.Is(e, eventlib.ErrIdempotency):
				status = 409
				message = "conflict"
			case errors.Is(e, command.ErrQueueUncertain):
				status = 503
				message = "queue acceptance uncertain; retry identical request"
			case errors.Is(e, eventlib.ErrUncertain):
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
			_ = Write(w, status, body)
		}
	}
}
