package dia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/mejango/blossm-toolbox-go/eventstore"
)

type operationState struct {
	Digest  string             `json:"digest"`
	Inputs  []eventstore.Input `json:"inputs"`
	Request json.RawMessage    `json:"request,omitempty"`
}
type operationReducer struct{}

func (operationReducer) Normalize(action string, raw json.RawMessage) (json.RawMessage, error) {
	if action != "commit" {
		return nil, ErrInvalid
	}
	var st operationState
	if e := decode(raw, &st); e != nil {
		return nil, e
	}
	if len(st.Digest) != 64 || len(st.Inputs) == 0 {
		return nil, ErrInvalid
	}
	return json.Marshal(st)
}
func (r operationReducer) Apply(state json.RawMessage, event eventstore.Event) (json.RawMessage, error) {
	if event.Number != 0 {
		return nil, errors.New("operation already committed")
	}
	return r.Normalize(event.Input.Action, event.Input.Payload)
}
func semanticDigest(events []eventstore.Input) string {
	copied := append([]eventstore.Input(nil), events...)
	for i := range copied {
		copied[i].Created = ""
		copied[i].Expected = nil
		copied[i].Transaction = eventstore.Transaction{}
	}
	b, _ := json.Marshal(copied)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// appendAtomic stores a receipt alongside all domain effects. Exact prepared inputs
// remain available after a lost response. A reused key with changed intent fails.
func (s *Service) appendAtomic(ctx context.Context, events []eventstore.Input) error {
	return s.appendAtomicRequest(ctx, events, nil, Ref{})
}

// appendAtomicRequest binds an explicitly supplied command request rather than
// derived targets. Inputs still retain the exact effects committed atomically.
func (s *Service) appendAtomicRequest(ctx context.Context, events []eventstore.Input, request json.RawMessage, receipt Ref) error {
	if len(events) == 0 {
		return ErrInvalid
	}
	r := receipt
	if r.ID == "" {
		r = s.root("core", "operation", stableID(events[0].Key, "receipt"))
	}
	var op operationState
	a, e := s.load(ctx, r, &op)
	if e != nil {
		return e
	}
	digest := semanticDigest(events)
	if len(request) > 0 {
		digest = requestDigest(request)
	}
	if a.Version > 0 {
		if op.Digest != digest {
			return eventstore.ErrIdempotency
		}
		return nil
	}
	op = operationState{Digest: digest, Inputs: events, Request: request}
	batch := append(append([]eventstore.Input(nil), events...), s.event(r, "commit", events[0].Key+"/receipt", 0, op))
	receipts, e := s.Store.Append(ctx, batch)
	if e != nil {
		// Backend labels do not decide whether another identical attempt committed.
		// A visible atomic receipt is proof; an absent/unreadable receipt is not.
		var committed operationState
		existing, lookupErr := s.load(ctx, r, &committed)
		if lookupErr == nil && existing.Version > 0 {
			if committed.Digest == digest {
				return nil
			}
			return eventstore.ErrIdempotency
		}
		return e
	}
	if len(receipts) != len(batch) {
		return errors.New("incomplete commit receipts")
	}
	for _, receipt := range receipts {
		if receipt.Outcome != eventstore.Committed && receipt.Outcome != eventstore.Duplicate {
			return eventstore.ErrUncertain
		}
	}
	return nil
}

func requestDigest(request json.RawMessage) string {
	h := sha256.Sum256(append([]byte("dia/explicit-command-request/v1\x00"), request...))
	return hex.EncodeToString(h[:])
}

func (s *Service) operation(ctx context.Context, root Ref, action, key string) (operationState, bool, error) {
	eventKey := stableID(root.Key(), action+"/"+key)
	var op operationState
	a, e := s.load(ctx, s.root("core", "operation", stableID(eventKey, "receipt")), &op)
	return op, a.Version > 0, e
}

// committedPayload checks a request whose first event wholly describes its input.
// Callers still authorize the requester before consulting this receipt.
func (s *Service) committedPayload(ctx context.Context, root Ref, action, key string, payload any) (bool, error) {
	op, exists, e := s.operation(ctx, root, action, key)
	if e != nil || !exists {
		return false, e
	}
	if len(op.Inputs) == 0 {
		return false, ErrInvalid
	}
	wanted, e := json.Marshal(payload)
	if e != nil {
		return false, e
	}
	wanted, e = eventstore.Canonical(wanted)
	if e != nil {
		return false, e
	}
	stored, e := eventstore.Canonical(op.Inputs[0].Payload)
	if e != nil {
		return false, e
	}
	if string(wanted) != string(stored) {
		return false, eventstore.ErrIdempotency
	}
	return true, nil
}
