package view

import (
	"context"
	"errors"
	"fmt"
	"testing"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

// Faults at each storage boundary must retain their classification, including
// errors whose text happens to sound like request validation.
type faultBackend struct {
	eventlib.Backend
	tx            *faultTx
	begin, commit error
}

func (b faultBackend) View(ctx context.Context, fn func(eventlib.Reader) error) error {
	if b.begin != nil {
		return b.begin
	}
	if err := fn(b.tx); err != nil {
		return err
	}
	return b.commit
}
func (b faultBackend) Update(ctx context.Context, fn func(eventlib.Tx) error) error {
	if b.begin != nil {
		return b.begin
	}
	if err := fn(b.tx); err != nil {
		return err
	}
	return b.commit
}

type faultTx struct {
	eventlib.Tx
	row                     eventlib.Projection
	read, lock, write, list error
}

func (tx *faultTx) Projection(context.Context, string) (eventlib.Projection, error) {
	return tx.row, tx.read
}
func (tx *faultTx) LockRoots(context.Context, []eventlib.Root) error { return tx.lock }
func (tx *faultTx) PutProjection(context.Context, eventlib.Projection) (bool, error) {
	return tx.write == nil, tx.write
}
func (tx *faultTx) Projections(context.Context, string, string, string, string, int) ([]eventlib.Projection, error) {
	return nil, tx.list
}
func validMutation() Mutation {
	return Mutation{ID: "a", Root: root("r"), Version: 2, Body: raw(`{"owner":"alice"}`)}
}
func validFaultTx() *faultTx {
	return &faultTx{row: eventlib.Projection{ID: "a", Root: root("r"), Version: 1, Body: raw(`{"owner":"alice","n":"text"}`)}}
}

func TestInvalidBoundaryClassification(t *testing.T) {
	s := Store{Scope: scope()}
	_, err := s.Get(context.Background(), "")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	for _, q := range []Query{{Limit: 10001}, {Offset: -1}, {Predicates: []eventlib.Predicate{predicate("x", `{}`)}}, {Predicates: []eventlib.Predicate{predicate("a..b", `1`)}}} {
		if _, err := s.List(context.Background(), q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("query %+v: %v", q, err)
		}
	}
	cases := []struct {
		name   string
		change func(*Mutation)
	}{
		{"identity", func(m *Mutation) { m.ID = "" }},
		{"scope", func(m *Mutation) { m.Root.Domain = "other" }},
		{"version", func(m *Mutation) { m.Version = -1 }},
		{"unsafe version", func(m *Mutation) { m.Version = 9007199254740992 }},
		{"expected", func(m *Mutation) { n := int64(-2); m.Expected = &n }},
		{"predicate", func(m *Mutation) { m.If = []eventlib.Predicate{predicate("x", `{}`)} }},
		{"body", func(m *Mutation) { m.Body = raw(`[]`) }},
		{"delete body", func(m *Mutation) { m.Delete = true }},
		{"tombstone owner", func(m *Mutation) { m.Tombstone = true; m.Body = raw(`{"owner":"bob"}`) }},
		{"tombstone delete", func(m *Mutation) { m.Tombstone = true; m.Delete = true }},
		{"patch expected", func(m *Mutation) { m.Body = nil; m.Operations = []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}} }},
		{"operation", func(m *Mutation) {
			n := int64(1)
			m.Expected = &n
			m.Body = nil
			m.Operations = []Operation{{Kind: "unknown", Path: "n"}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := validMutation()
			c.change(&m)
			if _, err := s.Batch(context.Background(), []Mutation{m}); !errors.Is(err, ErrInvalid) {
				t.Fatal(err)
			}
		})
	}
	for _, batch := range [][]Mutation{nil, {validMutation(), validMutation()}} {
		if _, err := s.Batch(context.Background(), batch); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
}

func TestBackendErrorsNeverBecomeInvalid(t *testing.T) {
	for _, cause := range []error{errors.New("invalid storage encoding"), eventlib.ErrUncertain, eventlib.ErrConflict, context.Canceled} {
		for _, stage := range []string{"begin", "commit", "read", "lock", "write", "list"} {
			t.Run(fmt.Sprintf("%s/%v", stage, cause), func(t *testing.T) {
				tx := validFaultTx()
				b := faultBackend{tx: tx}
				switch stage {
				case "begin":
					b.begin = cause
				case "commit":
					b.commit = cause
				case "read":
					tx.read = cause
				case "lock":
					tx.lock = cause
				case "write":
					tx.write = cause
				case "list":
					tx.list = cause
				}
				s := Store{Scope: scope(), Backend: b}
				var err error
				if stage == "list" {
					_, err = s.List(context.Background(), Query{})
				} else {
					_, err = s.Batch(context.Background(), []Mutation{validMutation()})
				}
				if !errors.Is(err, cause) || errors.Is(err, ErrInvalid) {
					t.Fatalf("lost storage classification: %v", err)
				}
				if stage == "begin" || stage == "commit" || stage == "read" {
					_, err = s.Get(context.Background(), "a")
					if !errors.Is(err, cause) || errors.Is(err, ErrInvalid) {
						t.Fatalf("read: %v", err)
					}
				}
			})
		}
	}
}

func TestOperationErrorsAndStoredCorruptionAreDistinct(t *testing.T) {
	for _, body := range []string{`{"owner":"alice","n":"text"}`, `{"owner":"alice","n":9007199254740991}`, `{"owner":"alice","n":9007199254740992}`, `{"owner":"alice",`} {
		tx := validFaultTx()
		tx.row.Body = raw(body)
		s := Store{Scope: scope(), Backend: faultBackend{tx: tx}}
		m := validMutation()
		m.Body = nil
		n := int64(1)
		m.Expected = &n
		m.Operations = []Operation{{Kind: Inc, Path: "n", Value: raw(`1`)}}
		_, err := s.Batch(context.Background(), []Mutation{m})
		wantInvalid := body == `{"owner":"alice","n":"text"}` || body == `{"owner":"alice","n":9007199254740991}`
		if err == nil || errors.Is(err, ErrInvalid) != wantInvalid {
			t.Fatalf("body %s: %v", body, err)
		}
	}
	s := Store{Scope: scope(), Backend: faultBackend{tx: validFaultTx()}}
	m := validMutation()
	n := int64(0)
	m.Expected = &n
	if _, err := s.Batch(context.Background(), []Mutation{m}); !errors.Is(err, eventlib.ErrConflict) || errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	m = validMutation()
	m.Body = raw(`{"owner":"bob"}`)
	if _, err := s.Batch(context.Background(), []Mutation{m}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	s.Scope.Owner = []eventlib.Predicate{predicate("owner", `{}`)}
	if _, err := s.List(context.Background(), Query{}); err == nil || errors.Is(err, ErrInvalid) {
		t.Fatalf("trusted configuration mislabeled: %v", err)
	}
}
