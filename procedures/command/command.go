// Package command executes validated commands against an atomic event appender.
// Queue acceptance, database commitment and callback completion are distinct.
package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mejango/blossm-toolbox-go/stores/event"
)

var (
	ErrInvalid        = errors.New("invalid command")
	ErrForbidden      = errors.New("required context missing")
	ErrQueueUncertain = errors.New("queue acceptance unconfirmed; retry the identical submission ID")
)

type Context map[string]json.RawMessage

// RequireContext checks presence/nonempty identity, not authorization. The
// authenticated transport must construct context, never trust a body claim.
func RequireContext(values Context, required []string) error {
	for _, name := range required {
		value, ok := values[name]
		if !ok || len(value) == 0 {
			return ErrForbidden
		}
		normalized, err := eventstore.Canonical(value)
		if err != nil {
			return ErrForbidden
		}
		switch string(normalized) {
		case "null", "false", "0", `""`:
			return ErrForbidden
		}
	}
	return nil
}

type Request struct {
	ID          string                 `json:"id"`
	Issued      string                 `json:"issued"`
	Root        *eventstore.Root       `json:"root,omitempty"`
	Payload     json.RawMessage        `json:"payload"`
	Context     Context                `json:"context,omitempty"`
	Claims      json.RawMessage        `json:"claims,omitempty"`
	Transaction eventstore.Transaction `json:"transaction"`
	Async       bool                   `json:"async,omitempty"`
}

type Appender interface {
	Append(context.Context, []eventstore.Input) ([]eventstore.Receipt, error)
}
type Submission struct {
	ID     string             `json:"id"`
	Events []eventstore.Input `json:"events"`
}
type Acceptance struct {
	ID string `json:"id"`
	// EnvelopeID identifies immutable queued content for authorized outcome lookup.
	// ID remains the provider acceptance identifier.
	EnvelopeID string `json:"envelopeId,omitempty"`
}

// Queue returns a nonempty provider acceptance ID only after acknowledgment.
// An error may still mean an accepted message; stable event keys make redelivery safe.
type Queue interface {
	Enqueue(context.Context, Submission) (Acceptance, error)
}

type Contribution struct {
	Body       map[string]json.RawMessage `json:"body,omitempty"`
	Headers    map[string]string          `json:"headers,omitempty"`
	Tokens     []json.RawMessage          `json:"tokens,omitempty"`
	Revoke     []string                   `json:"revoke,omitempty"`
	StatusCode int                        `json:"statusCode,omitempty"`
}

type Result struct {
	Events   []eventstore.Input
	Response Contribution
	// Then receives all outcomes in input order, including an empty slice.
	// It is awaited but not durable. Gate side effects explicitly on COMMITTED;
	// deterministic response construction also runs for DUPLICATE and QUEUED.
	Then func(context.Context, []Receipt) (Contribution, error)
}

type Receipt struct {
	Outcome eventstore.Outcome `json:"outcome"`
	Input   eventstore.Input   `json:"input"`
	Event   *eventstore.Event  `json:"event,omitempty"`
}

type Response struct {
	Contribution
	ID            string      `json:"id"`
	TransactionID string      `json:"transactionId"`
	Receipts      []Receipt   `json:"receipts"`
	Acceptance    *Acceptance `json:"acceptance,omitempty"`
}

// CallbackError preserves confirmed outcomes, including queue acceptance.
// It never means that an earlier commit or provider acceptance was rolled back.
type CallbackError struct{ Err error }

func (e *CallbackError) Error() string {
	return "command outcomes recorded; best-effort callback failed: " + e.Err.Error()
}
func (e *CallbackError) Unwrap() error { return e.Err }

type Procedure struct {
	Store            Appender
	Queue            Queue
	Local            bool
	RequiredContexts []string
	RequiresRoot     bool
	Trace            *eventstore.Trace
	Validate         func(context.Context, Request) error
	Fill             func(context.Context, json.RawMessage) (json.RawMessage, error)
	Normalize        func(context.Context, json.RawMessage) (json.RawMessage, error)
	Main             func(context.Context, Request) (Result, error)
}

func (p Procedure) Execute(ctx context.Context, in Request) (Response, error) {
	response := Response{ID: in.ID, Receipts: []Receipt{}}
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if p.Main == nil || strings.TrimSpace(in.ID) == "" || len(in.ID) > 1024 {
		return response, ErrInvalid
	}
	issued, err := time.Parse(time.RFC3339Nano, in.Issued)
	if err != nil {
		return response, fmt.Errorf("%w: issued timestamp required", ErrInvalid)
	}
	in.Issued = issued.UTC().Format(time.RFC3339Nano)
	if err = RequireContext(in.Context, p.RequiredContexts); err != nil {
		return response, err
	}
	if p.RequiresRoot && (in.Root == nil || in.Root.ID == "") {
		return response, fmt.Errorf("%w: root required", ErrInvalid)
	}
	if len(in.Payload) == 0 {
		in.Payload = json.RawMessage(`{}`)
	}
	in.Payload, err = eventstore.Canonical(in.Payload)
	if err != nil {
		return response, fmt.Errorf("%w: payload", ErrInvalid)
	}
	// Validation precedes fill/normalization, matching the retained procedure contract.
	if p.Validate != nil {
		if err = p.Validate(ctx, in); err != nil {
			return response, err
		}
	}
	for _, transform := range []func(context.Context, json.RawMessage) (json.RawMessage, error){p.Fill, p.Normalize} {
		if transform != nil {
			in.Payload, err = transform(ctx, in.Payload)
			if err != nil {
				return response, err
			}
		}
	}
	in.Payload, err = eventstore.Canonical(in.Payload)
	if err != nil {
		return response, fmt.Errorf("%w: transformed payload", ErrInvalid)
	}
	if in.Transaction.ID == "" {
		in.Transaction.ID = in.ID
	}
	if in.Transaction.Issued == "" {
		in.Transaction.Issued = in.Issued
	}
	if len(in.Claims) > 0 {
		in.Transaction.Claims = append(json.RawMessage(nil), in.Claims...)
	}
	in.Transaction.Path = append([]eventstore.Trace(nil), in.Transaction.Path...)
	if p.Trace != nil {
		in.Transaction.Path = append(in.Transaction.Path, *p.Trace)
	}
	response.TransactionID = in.Transaction.ID
	result, err := p.Main(ctx, in)
	if err != nil {
		return response, err
	}
	if err = validContribution(result.Response); err != nil {
		return response, err
	}
	response.Contribution = merge(Contribution{}, result.Response)
	contextJSON, err := json.Marshal(in.Context)
	if err != nil {
		return response, fmt.Errorf("%w: context", ErrInvalid)
	}
	if in.Context == nil {
		contextJSON = []byte(`{}`)
	}
	events := append([]eventstore.Input(nil), result.Events...)
	conditional := false
	for i := range events {
		event := &events[i]
		if event.Root == (eventstore.Root{}) && in.Root != nil {
			event.Root = *in.Root
		}
		if event.Key == "" {
			actor, actorErr := actorIdentity(in)
			if actorErr != nil {
				return response, fmt.Errorf("%w: actor identity: %w", ErrInvalid, actorErr)
			}
			event.Key, err = eventstore.Digest([]any{"blossm.command.event.v2", actor, in.ID, i})
			if err != nil {
				return response, err
			}
		}
		// The immutable request envelope owns time/trace for exact retries.
		event.Created = in.Issued
		event.Transaction = in.Transaction
		if len(event.Context) == 0 {
			event.Context = append(json.RawMessage(nil), contextJSON...)
		}
		if len(event.Payload) == 0 {
			event.Payload = json.RawMessage(`{}`)
		}
		conditional = conditional || event.Expected != nil
	}
	if len(events) > 0 && in.Async && !p.Local && !conditional {
		if p.Queue == nil {
			return response, errors.New("command queue not configured")
		}
		accepted, err := p.Queue.Enqueue(ctx, Submission{ID: in.ID, Events: events})
		if err != nil {
			return response, errors.Join(ErrQueueUncertain, err)
		}
		if accepted.ID == "" {
			return response, ErrQueueUncertain
		}
		response.Acceptance = &accepted
		for _, event := range events {
			response.Receipts = append(response.Receipts, Receipt{Outcome: eventstore.Queued, Input: event})
		}
	} else if len(events) > 0 {
		if p.Store == nil {
			return response, errors.New("command store not configured")
		}
		receipts, err := p.Store.Append(ctx, events)
		if err != nil {
			return response, err
		}
		if len(receipts) != len(events) {
			return response, errors.New("append returned incomplete receipts")
		}
		for i, receipt := range receipts {
			if receipt.Outcome != eventstore.Committed && receipt.Outcome != eventstore.Duplicate {
				return response, errors.New("append returned unconfirmed outcome")
			}
			persisted := receipt.Event
			if persisted.Input.Key != events[i].Key {
				return response, errors.New("append receipt identity mismatch")
			}
			response.Receipts = append(response.Receipts, Receipt{Outcome: receipt.Outcome, Input: events[i], Event: &persisted})

		}
	}
	if result.Then != nil {
		if err := ctx.Err(); err != nil {
			return response, &CallbackError{Err: err}
		}
		outcomes := make([]Receipt, len(response.Receipts))
		for i, receipt := range response.Receipts {
			receipt.Input = eventstore.CloneEvent(eventstore.Event{Input: receipt.Input}).Input
			if receipt.Event != nil {
				event := eventstore.CloneEvent(*receipt.Event)
				receipt.Event = &event
			}
			outcomes[i] = receipt
		}
		contribution, callbackErr := result.Then(ctx, outcomes)
		if callbackErr == nil {
			callbackErr = validContribution(contribution)
		}
		if callbackErr != nil {
			return response, &CallbackError{Err: callbackErr}
		}
		response.Contribution = merge(response.Contribution, contribution)
	}
	if response.StatusCode == 0 {
		response.StatusCode = 202
		if len(events) == 0 {
			response.StatusCode = 204
			if len(response.Body) > 0 || len(response.Tokens) > 0 || len(response.Revoke) > 0 {
				response.StatusCode = 200
			}
		}
	}
	return response, nil
}

// Consume commits acknowledged queue payloads. It never reruns command Main or
// serializes callbacks; delivery is at least once and event keys own deduplication.
func Consume(ctx context.Context, store Appender, submission Submission) ([]eventstore.Receipt, error) {
	if store == nil || submission.ID == "" || len(submission.Events) == 0 {
		return nil, ErrInvalid
	}
	return store.Append(ctx, submission.Events)
}

func validContribution(c Contribution) error {
	if c.StatusCode != 0 && (c.StatusCode < 200 || c.StatusCode > 599) {
		return fmt.Errorf("%w: response status", ErrInvalid)
	}
	for _, v := range c.Body {
		if _, err := eventstore.Canonical(v); err != nil {
			return fmt.Errorf("%w: response body", ErrInvalid)
		}
	}
	for _, v := range c.Tokens {
		if _, err := eventstore.Canonical(v); err != nil {
			return fmt.Errorf("%w: response token", ErrInvalid)
		}
	}
	return nil
}
func merge(base, add Contribution) Contribution {
	if base.Body == nil {
		base.Body = map[string]json.RawMessage{}
	}
	for k, v := range add.Body {
		base.Body[k] = append(json.RawMessage(nil), v...)
	}
	if base.Headers == nil {
		base.Headers = map[string]string{}
	}
	for k, v := range add.Headers {
		base.Headers[k] = v
	}
	base.Tokens = append(base.Tokens, add.Tokens...)
	base.Revoke = append(base.Revoke, add.Revoke...)
	if add.StatusCode != 0 {
		base.StatusCode = add.StatusCode
	}
	return base
}

// actorIdentity namespaces generated keys, not authorization or event fingerprints.
// Context.principal is already authenticated by the consumer boundary. Without it,
// only issuer/audience/subject identify an actor; credential lifetime is irrelevant.
func actorIdentity(in Request) (json.RawMessage, error) {
	if principal, ok := in.Context["principal"]; ok {
		canonical, err := eventstore.Canonical(principal)
		if err != nil {
			return nil, err
		}
		return eventstore.Encode([]any{"principal", canonical})
	}
	var claims map[string]json.RawMessage
	if len(in.Transaction.Claims) > 0 {
		if err := eventstore.Decode(in.Transaction.Claims, &claims); err != nil {
			return nil, err
		}
		if claims == nil {
			return nil, errors.New("claims must be an object")
		}
	}
	identity := map[string]json.RawMessage{}
	for _, name := range []string{"iss", "aud", "sub"} {
		if value, ok := claims[name]; ok {
			identity[name] = value
		}
	}
	return eventstore.Encode([]any{"claims", identity})
}
