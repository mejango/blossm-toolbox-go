// Package event owns the representation shared by storage, replay and publication.
package event

import (
	"context"
	"encoding/json"
	"errors"
)

var (
	ErrInvalid     = errors.New("invalid event input")
	ErrConflict    = errors.New("event number conflict")
	ErrIdempotency = errors.New("idempotency key reused with different content")
	ErrNotFound    = errors.New("not found")
	ErrUncertain   = errors.New("commit outcome unknown; retry the identical request key")
)

type Root struct {
	Network string `json:"network"`
	Service string `json:"service"`
	Domain  string `json:"domain"`
	ID      string `json:"id"`
}

func (r Root) Key() string { b, _ := json.Marshal(r); return string(b) }

type Trace struct {
	Network   string `json:"network"`
	Host      string `json:"host"`
	Procedure string `json:"procedure"`
	Hash      string `json:"hash"`
	Name      string `json:"name,omitempty"`
}
type Transaction struct {
	ID     string          `json:"id"`
	Issued string          `json:"issued"`
	Path   []Trace         `json:"path"`
	Claims json.RawMessage `json:"claims,omitempty"`
}
type Input struct {
	Root        Root            `json:"root"`
	Action      string          `json:"action"`
	Key         string          `json:"key"`
	Expected    *int64          `json:"expected,omitempty"`
	Created     string          `json:"created"`
	Payload     json.RawMessage `json:"payload"`
	Context     json.RawMessage `json:"context,omitempty"`
	Transaction Transaction     `json:"transaction"`
}
type Event struct {
	Input       Input  `json:"input"`
	Number      int64  `json:"number"`
	Hash        string `json:"hash"`
	Fingerprint string `json:"fingerprint"`
}
type Outcome string

const (
	Committed Outcome = "COMMITTED"
	Duplicate Outcome = "DUPLICATE"
	Queued    Outcome = "QUEUED"
)

type Receipt struct {
	Outcome Outcome `json:"outcome"`
	Event   Event   `json:"event"`
}
type Publication struct {
	ID       string `json:"id"`
	Event    Event  `json:"event"`
	Attempts int64  `json:"attempts"`
}
type Snapshot struct {
	Root      Root            `json:"root"`
	Number    int64           `json:"number"`
	LastEvent int64           `json:"lastEvent"`
	State     json.RawMessage `json:"state"`
	Events    []byte          `json:"events"`
	Hash      string          `json:"hash"`
}
type Block struct {
	Number       int64  `json:"number"`
	Previous     string `json:"previous"`
	Events       []byte `json:"events"`
	Snapshots    []byte `json:"snapshots"`
	Transactions []byte `json:"transactions"`
	Hash         string `json:"hash"`
	Signature    []byte `json:"signature,omitempty"`
}

// Reader callbacks are serial, awaited and cancellation-aware. Implementations
// must keep one repeatable transaction snapshot until the callback returns.
type Reader interface {
	Find(context.Context, string) (Event, error)
	Count(context.Context, Root) (int64, error)
	Events(context.Context, Root, int64, func(Event) error) error
	Roots(context.Context, func(Root) error) error
	Snapshot(context.Context, Root) (Snapshot, error)
	LastBlock(context.Context) (Block, error)
	Pending(context.Context, int) ([]Publication, error)
	Projection(context.Context, string) (Projection, error)
}
type Tx interface {
	Reader
	// LockRoots acquires deterministic root order before mutation.
	LockRoots(context.Context, []Root) error
	PutCount(context.Context, Root, int64) error
	PutEvent(context.Context, Event) error
	PutPublication(context.Context, Publication) error
	DeletePublication(context.Context, string) error
	FailPublication(context.Context, string) error
	PutSnapshot(context.Context, Snapshot) error
	PutBlock(context.Context, Block) error
	PutProjection(context.Context, Projection) (bool, error)
}
type Backend interface {
	View(context.Context, func(Reader) error) error
	Update(context.Context, func(Tx) error) error
	Close(context.Context) error
}
type Projection struct {
	ID      string          `json:"id"`
	Root    Root            `json:"root"`
	Version int64           `json:"version"`
	Body    json.RawMessage `json:"body"`
	Deleted bool            `json:"deleted"`
}

// Reducer validates payloads before commitment and state after deterministic replay.
type Reducer interface {
	Normalize(action string, payload json.RawMessage) (json.RawMessage, error)
	Apply(state json.RawMessage, event Event) (json.RawMessage, error)
}
type Publisher interface {
	Publish(context.Context, Publication) error
}

type Predicate struct {
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value"`
}

// CandidateReader narrows clean snapshots while retaining every dirty root.
// Implementations may return false positives, never omit a possible match.
type CandidateReader interface {
	Candidates(context.Context, string, string, string, []Predicate) ([]Root, error)
}

type Signer interface {
	Sign(context.Context, []byte) ([]byte, error)
}

type FrontierReader interface {
	PendingRoots(context.Context, int) ([]Root, error)
}

// Verifier must verify the configured producer identity; a self-supplied public
// key from an untrusted block is never an authority source.
type Verifier interface {
	Verify(context.Context, []byte, []byte) error
}

type Aggregate struct {
	Root    Root            `json:"root"`
	Version int64           `json:"version"`
	State   json.RawMessage `json:"state"`
}
