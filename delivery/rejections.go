package delivery

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"unicode/utf8"

	"blossm.local/backend/command"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/view"
)

type RejectionReason string

const (
	RejectionInvalid     RejectionReason = "invalid"
	RejectionIdempotency RejectionReason = "idempotency"
	RejectionConflict    RejectionReason = "conflict"
	RejectionForbidden   RejectionReason = "forbidden"
)

// Rejection deliberately retains no event payload, identity claims or error text.
// EnvelopeID binds the complete immutable delivery; Reason is a fixed code.
type Rejection struct {
	EnvelopeID string          `json:"envelopeId"`
	Reason     RejectionReason `json:"reason"`
}

// Rejections must return only durably confirmed records. Lookup returns
// eventstore.ErrNotFound when absent. Record is immutable and exactly idempotent;
// its bool is true only for a newly inserted, confirmed record. It is false for
// duplicates and every error, including a possibly committed uncertain result.
type Rejections interface {
	Lookup(context.Context, string) (Rejection, error)
	Record(context.Context, Rejection) (Rejection, bool, error)
}

var ErrRejectionMismatch = errors.New("delivery rejection record mismatch")

func (r Rejection) valid() bool {
	if len(r.EnvelopeID) != 64 {
		return false
	}
	id, err := hex.DecodeString(r.EnvelopeID)
	if err != nil || len(id) != 32 || hex.EncodeToString(id) != r.EnvelopeID {
		return false
	}
	switch r.Reason {
	case RejectionInvalid, RejectionIdempotency, RejectionConflict, RejectionForbidden:
		return true
	}
	return false
}

// RejectionJournal uses an explicit internal scope per consumer. Do not expose
// its reserved named view through public view routes. Source event roots and
// source payloads never enter the record. Backend/client lifetime is caller-owned.
type RejectionJournal struct {
	Backend eventstore.Backend
	Scope   eventstore.Root
}

func (j RejectionJournal) location(id string) (string, eventstore.Root, error) {
	if j.Backend == nil || !(Rejection{EnvelopeID: id, Reason: RejectionInvalid}).valid() {
		return "", eventstore.Root{}, fmt.Errorf("rejection backend and valid envelope ID required")
	}
	for _, field := range []string{j.Scope.Network, j.Scope.Service, j.Scope.Domain, j.Scope.ID} {
		if strings.TrimSpace(field) == "" || len(field) > 1024 || !utf8.ValidString(field) {
			return "", eventstore.Root{}, fmt.Errorf("explicit rejection scope required")
		}
	}
	root := j.Scope
	root.ID += "/" + id
	return view.PhysicalID("$delivery/rejections/"+j.Scope.Key(), id), root, nil
}
func decodeRejection(p eventstore.Projection, id string, root eventstore.Root) (Rejection, error) {
	var record Rejection
	if p.Root != root || p.Version != 0 || p.Deleted {
		return record, ErrRejectionMismatch
	}
	if err := eventstore.Decode(p.Body, &record); err != nil {
		return record, ErrRejectionMismatch
	}
	if !record.valid() || record.EnvelopeID != id {
		return Rejection{}, ErrRejectionMismatch
	}
	return record, nil
}
func (j RejectionJournal) Lookup(ctx context.Context, id string) (Rejection, error) {
	key, root, err := j.location(id)
	if err != nil {
		return Rejection{}, err
	}
	var result Rejection
	err = j.Backend.View(ctx, func(reader eventstore.Reader) error {
		p, err := reader.Projection(ctx, key)
		if err != nil {
			return err
		}
		if p.ID != key {
			return ErrRejectionMismatch
		}
		result, err = decodeRejection(p, id, root)
		return err
	})
	if err != nil {
		return Rejection{}, err
	}
	return result, nil
}
func (j RejectionJournal) Record(ctx context.Context, record Rejection) (Rejection, bool, error) {
	if !record.valid() {
		return Rejection{}, false, fmt.Errorf("invalid rejection record")
	}
	key, root, err := j.location(record.EnvelopeID)
	if err != nil {
		return Rejection{}, false, err
	}
	body, err := eventstore.Encode(record)
	if err != nil {
		return Rejection{}, false, err
	}
	fresh := false
	err = j.Backend.Update(ctx, func(tx eventstore.Tx) error {
		// A known-abort retry may find an insertion committed by another worker.
		fresh = false
		if err := tx.LockRoots(ctx, []eventstore.Root{root}); err != nil {
			return err
		}
		existing, err := tx.Projection(ctx, key)
		if err == nil {
			if existing.ID != key {
				return ErrRejectionMismatch
			}
			previous, err := decodeRejection(existing, record.EnvelopeID, root)
			if err != nil {
				return err
			}
			if previous != record {
				return ErrRejectionMismatch
			}
			return nil
		}
		if !errors.Is(err, eventstore.ErrNotFound) || retryRequired(err) {
			return err
		}
		applied, err := tx.PutProjection(ctx, eventstore.Projection{ID: key, Root: root, Version: 0, Body: body})
		if err != nil {
			return err
		}
		if !applied {
			return ErrRejectionMismatch
		}
		fresh = true
		return nil
	})
	if err != nil {
		return Rejection{}, false, err
	}
	return record, fresh, nil
}

// Any retry/unknown signal takes precedence over a joined deterministic marker.
// Do not import provider SDKs to interpret their individual error codes here.
func retryRequired(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, eventstore.ErrUncertain) {
		return true
	}
	// A bare transport sentinel may not implement net.Error after wrapping.
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF, io.ErrClosedPipe, net.ErrClosed, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.ETIMEDOUT, syscall.EPIPE, syscall.ENETUNREACH, syscall.EHOSTUNREACH} {
		if errors.Is(err, cause) {
			return true
		}
	}
	var network net.Error
	if errors.As(err, &network) {
		return true
	}
	if labeled, ok := err.(interface{ HasErrorLabel(string) bool }); ok {
		for _, label := range []string{"NetworkError", "TransientTransactionError", "UnknownTransactionCommitResult", "RetryableWriteError"} {
			if labeled.HasErrorLabel(label) {
				return true
			}
		}
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range joined.Unwrap() {
			if retryRequired(cause) {
				return true
			}
		}
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return retryRequired(wrapped.Unwrap())
	}
	return false
}
func (c Consumer) rejectionReason(err error) RejectionReason {
	if err == nil || retryRequired(err) {
		return ""
	}
	switch {
	case errors.Is(err, eventstore.ErrIdempotency):
		return RejectionIdempotency
	case errors.Is(err, eventstore.ErrConflict):
		return RejectionConflict
	case errors.Is(err, ErrInvalid) || errors.Is(err, command.ErrInvalid) || errors.Is(err, eventstore.ErrInvalid) || errors.Is(err, view.ErrInvalid):
		return RejectionInvalid
	case c.Forbidden != nil && c.Forbidden(err):
		return RejectionForbidden
	}
	return ""
}

var _ Rejections = RejectionJournal{}
