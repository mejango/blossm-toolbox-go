// Package delivery binds prepared commands and projection work to existing cloud
// adapters. Provider acceptance, durable consumption and receipt acknowledgment
// are separate boundaries. Queue credentials and clients remain caller-owned.
package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"blossm.local/backend/command"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/projection"
)

// MaxEnvelopeBytes is a portable application bound, not a provider capacity claim.
const MaxEnvelopeBytes = 256 << 10

var ErrInvalid = errors.New("invalid delivery envelope")
var ErrEnqueueUncertain = errors.New("delivery enqueue unconfirmed; retry identical envelope")

type Envelope struct {
	Version     int                     `json:"version"`
	ID          string                  `json:"id"`
	Command     *command.Submission     `json:"command,omitempty"`
	Replay      *projection.Request     `json:"replay,omitempty"`
	Publication *eventstore.Publication `json:"publication,omitempty"`
}

func (e Envelope) validate() error {
	if e.Version != 1 || len(e.ID) != 64 {
		return ErrInvalid
	}
	n := 0
	if e.Command != nil {
		n++
		if strings.TrimSpace(e.Command.ID) == "" || len(e.Command.ID) > 1024 || len(e.Command.Events) == 0 {
			return ErrInvalid
		}
	}
	if e.Replay != nil {
		n++
		r := e.Replay.Root
		if e.Replay.Event != nil || e.Replay.RunID.Validate() != nil || r.Network == "" || r.Service == "" || r.Domain == "" || r.ID == "" {
			return ErrInvalid
		}
	}
	if e.Publication != nil {
		n++
		if e.Publication.ID == "" || e.Publication.ID != e.Publication.Event.Hash || e.Publication.Attempts != 0 {
			return ErrInvalid
		}
		if err := eventstore.ValidatePublicationSize(*e.Publication); err != nil {
			return errors.Join(ErrInvalid, err)
		}
	}
	if n != 1 {
		return ErrInvalid
	}
	expected, err := e.identity()
	if err != nil || expected != e.ID {
		return ErrInvalid
	}
	return nil
}
func (e Envelope) identity() (string, error) {
	e.ID = ""
	return eventstore.Digest(e)
}

// Encode prepares a stable, bounded provider payload. ID and Version are derived.
func Encode(e Envelope) ([]byte, error) {
	e.Version = 1
	var err error
	e.ID, err = e.identity()
	if err != nil {
		return nil, errors.Join(ErrInvalid, err)
	}
	if err = e.validate(); err != nil {
		return nil, err
	}
	body, err := eventstore.Encode(e)
	if err != nil {
		return nil, errors.Join(ErrInvalid, err)
	}
	if len(body) > MaxEnvelopeBytes {
		return nil, fmt.Errorf("%w: exceeds %d bytes", ErrInvalid, MaxEnvelopeBytes)
	}
	return body, nil
}
func Decode(body []byte) (Envelope, error) {
	var e Envelope
	if len(body) == 0 || len(body) > MaxEnvelopeBytes {
		return e, ErrInvalid
	}
	if err := eventstore.Decode(json.RawMessage(body), &e); err != nil {
		return e, errors.Join(ErrInvalid, err)
	}

	return e, e.validate()
}
func commandBody(s command.Submission) ([]byte, error) { return Encode(Envelope{Command: &s}) }
func replayBody(r projection.Request) ([]byte, error) {
	return Encode(Envelope{Replay: &r})
}
func publicationBody(p eventstore.Publication) ([]byte, error) {
	if p.Attempts < 0 {
		return nil, ErrInvalid
	}
	// Delivery bookkeeping changes across outbox retries, identity does not.
	p.Attempts = 0
	return Encode(Envelope{Publication: &p})
}
func enqueueError(err error) error {
	return errors.Join(ErrEnqueueUncertain, command.ErrQueueUncertain, err)
}
func preflight(ctx context.Context, body []byte) (Envelope, error) {
	if err := ctx.Err(); err != nil {
		return Envelope{}, err
	}
	return Decode(body)
}

// commandAcceptance exposes content identity separately from provider acceptance.
func commandAcceptance(body []byte, providerID string, sendErr error) (command.Acceptance, error) {
	envelope, err := Decode(body)
	if err != nil {
		return command.Acceptance{}, err
	}
	result := command.Acceptance{EnvelopeID: envelope.ID}
	if sendErr != nil {
		return result, sendErr
	}
	result.ID = providerID
	return result, nil
}
