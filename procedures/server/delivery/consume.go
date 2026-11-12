package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/projection"
)

type Consumer struct {
	// OnRejected is awaited after a confirmed fresh journal insertion, before ack.
	// It receives no private payload. Errors cannot undo the durable outcome.
	// It is best-effort: process death or an uncertain commit can lose a notice.
	OnRejected func(context.Context, Rejection) error
	// Rejections enables durable terminal handling; nil leaves failures unacknowledged.
	Rejections Rejections
	// Forbidden classifies application authorization failures, never transient errors.
	Forbidden func(error) bool
	// Admit must enforce allowed source roots/scopes and command operations.
	// It is required even when transport authentication has already succeeded.
	Admit        func(context.Context, Envelope) error
	Commands     command.Appender
	Projections  *projection.Engine
	Publications *projection.PublicationAdapter
}
type Result struct {
	// ObservationError reports best-effort hook failure, never a commit failure.
	ObservationError error
	// Rejected is set only after a durably confirmed terminal record.
	Rejected   *Rejection
	Receipts   []eventlib.Receipt
	Projection *projection.Result
}

// Consume accepts only trusted queue content. Callers must restrict queue writers
// and HTTP identity/route authority; body claims cannot authenticate a producer.
func (c Consumer) Consume(ctx context.Context, body []byte) (response Result, err error) {
	e, err := preflight(ctx, body)
	if err != nil {
		return Result{}, err
	}
	if c.Admit == nil {
		return Result{}, fmt.Errorf("delivery admission policy required")
	}
	// A terminal record fences later redelivery even if policy changes.
	if c.Rejections != nil {
		recorded, lookupErr := c.Rejections.Lookup(ctx, e.ID)
		if lookupErr == nil {
			if !recorded.valid() || recorded.EnvelopeID != e.ID {
				return Result{}, ErrRejectionMismatch
			}
			if err = ctx.Err(); err != nil {
				return Result{}, err
			}
			return Result{Rejected: &recorded}, nil
		}
		if !errors.Is(lookupErr, eventlib.ErrNotFound) || retryRequired(lookupErr) {
			return Result{}, lookupErr
		}
	}
	// A structurally valid envelope has a verified immutable ID. Malformed input
	// above this boundary cannot produce a trustworthy terminal journal key.
	defer func() {
		if err == nil || c.Rejections == nil || ctx.Err() != nil {
			return
		}
		if response.Projection != nil && (response.Projection.Committed || response.Projection.Uncertain) {
			return
		}
		reason := c.rejectionReason(err)
		if reason == "" {
			return
		}
		expected := Rejection{EnvelopeID: e.ID, Reason: reason}
		recorded, fresh, recordErr := c.Rejections.Record(ctx, expected)
		if recordErr != nil {
			err = errors.Join(err, recordErr)
			return
		}
		if recorded != expected || !recorded.valid() {
			err = errors.Join(err, ErrRejectionMismatch)
			return
		}
		if canceled := ctx.Err(); canceled != nil {
			err = errors.Join(err, canceled)
			return
		}
		response = Result{Rejected: &recorded}
		err = nil
		if fresh && c.OnRejected != nil {
			response.ObservationError = c.OnRejected(ctx, recorded)
		}
	}()
	if err = c.Admit(ctx, e); err != nil {
		return Result{}, err
	}
	// Admission observes a detached image; its mutations are not executed.
	e, err = Decode(body)
	if err != nil {
		return Result{}, err
	}
	if err = ctx.Err(); err != nil {
		return Result{}, err
	}
	if e.Command != nil {
		receipts, err := command.Consume(ctx, c.Commands, *e.Command)
		if err != nil {
			return Result{}, err
		}
		if len(receipts) != len(e.Command.Events) {
			return Result{}, fmt.Errorf("unconfirmed command receipt count")
		}
		for i, r := range receipts {
			if (r.Outcome != eventlib.Committed && r.Outcome != eventlib.Duplicate) || r.Event.Input.Key != e.Command.Events[i].Key {
				return Result{}, fmt.Errorf("unconfirmed command receipt identity/outcome")
			}
		}
		return Result{Receipts: receipts}, nil
	}
	var result projection.Result
	if e.Replay != nil {
		if c.Projections == nil {
			return Result{}, fmt.Errorf("projection engine required")
		}
		result, err = c.Projections.Play(ctx, *e.Replay)
	} else {
		if c.Publications == nil {
			return Result{}, fmt.Errorf("publication adapter required")
		}
		result, err = c.Publications.Consume(ctx, *e.Publication)
	}
	// A successful no-op (ignored source, empty plan or stale version) is ackable.
	// PushFailures remain in the result and never cause durable work redelivery.
	return Result{Projection: &result}, err
}

// Handler is a POST consume endpoint. Authorize must verify issuer, audience and
// allowed producer identity/route, not merely the presence of a bearer header.
// A nil authenticator fails construction. Confirmed work or durable rejection
// yields2xx; terminal rejection is explicit in the response headers.
func (c Consumer) Handler(authorize func(*http.Request) error) (http.Handler, error) {
	if c.Admit == nil {
		return nil, fmt.Errorf("delivery admission policy required")
	}
	if authorize == nil {
		return nil, fmt.Errorf("delivery authenticator required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := authorize(r); err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxEnvelopeBytes))
		if err != nil {
			http.Error(w, "invalid delivery body", http.StatusRequestEntityTooLarge)
			return
		}
		if _, err = Decode(body); err != nil {
			http.Error(w, "invalid delivery envelope", http.StatusBadRequest)
			return
		}
		result, err := c.Consume(r.Context(), body)
		if err != nil {
			http.Error(w, "delivery not committed", http.StatusServiceUnavailable)
			return
		}
		if r.Context().Err() != nil {
			http.Error(w, "delivery acknowledgment canceled", http.StatusServiceUnavailable)
			return
		}
		if result.Rejected != nil {
			w.Header().Set("X-Blossm-Rejection", result.Rejected.EnvelopeID)
			w.Header().Set("X-Blossm-Rejection-Reason", string(result.Rejected.Reason))
		}
		w.WriteHeader(http.StatusNoContent)
	}), nil
}
