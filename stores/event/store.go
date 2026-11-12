package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type Store struct {
	Backend  Backend
	Reducers map[string]Reducer
	Local    bool
}

func (s *Store) reducer(r Root) (Reducer, error) {
	v, ok := s.Reducers[r.Service+"/"+r.Domain]
	if !ok {
		return nil, fmt.Errorf("unregistered domain %s/%s", r.Service, r.Domain)
	}
	return v, nil
}
func validateRoot(r Root) error {
	for _, v := range []string{r.Network, r.Service, r.Domain, r.ID} {
		if strings.TrimSpace(v) == "" || len(v) > 512 || !utf8.ValidString(v) {
			return fmt.Errorf("invalid root")
		}
	}
	return nil
}
func timestamp(v string) (string, error) {
	t, e := time.Parse(time.RFC3339Nano, v)
	if e != nil {
		return "", fmt.Errorf("invalid timestamp")
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}
func (s *Store) normalize(in Input) (Input, error) {
	in = cloneInput(in)
	if err := validateRoot(in.Root); err != nil {
		return in, err
	}
	if in.Key == "" || len(in.Key) > 1024 || in.Action == "" || in.Transaction.ID == "" || !utf8.ValidString(in.Key) || !utf8.ValidString(in.Action) || !utf8.ValidString(in.Transaction.ID) {
		return in, fmt.Errorf("required event identity missing")
	}
	if in.Expected != nil && (*in.Expected < 0 || *in.Expected > 9007199254740991) {
		return in, fmt.Errorf("invalid expected number")
	}
	var err error
	if in.Created, err = timestamp(in.Created); err != nil {
		return in, err
	}
	if in.Transaction.Issued, err = timestamp(in.Transaction.Issued); err != nil {
		return in, err
	}
	for _, t := range in.Transaction.Path {
		for _, field := range []string{t.Network, t.Host, t.Procedure, t.Hash, t.Name} {
			if !utf8.ValidString(field) {
				return in, fmt.Errorf("invalid transaction trace UTF-8")
			}
		}
		if t.Network == "" || t.Host == "" || t.Procedure == "" || t.Hash == "" {
			return in, fmt.Errorf("incomplete transaction trace")
		}
	}
	if in.Transaction.Path == nil {
		in.Transaction.Path = []Trace{}
	}
	if len(in.Transaction.Claims) > 0 {
		in.Transaction.Claims, err = Canonical(in.Transaction.Claims)
		if err != nil {
			return in, err
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(in.Transaction.Claims, &obj) != nil || obj == nil {
			return in, fmt.Errorf("claims must be object")
		}
		for _, name := range []string{"exp", "iat", "nbf"} {
			if raw, ok := obj[name]; ok {
				var seconds int64
				if json.Unmarshal(raw, &seconds) != nil || seconds < 0 {
					return in, fmt.Errorf("claim %s must be nonnegative epoch seconds", name)
				}
			}
		}
		if raw, ok := obj["aud"]; ok {
			var single string
			if json.Unmarshal(raw, &single) != nil {
				var many []string
				if json.Unmarshal(raw, &many) != nil || len(many) == 0 {
					return in, fmt.Errorf("audience must be string or string array")
				}
				for _, a := range many {
					if a == "" {
						return in, fmt.Errorf("empty audience")
					}
				}
			} else if single == "" {
				return in, fmt.Errorf("empty audience")
			}
		}

	}
	if len(in.Context) == 0 {
		in.Context = json.RawMessage(`{}`)
	}
	if in.Context, err = Canonical(in.Context); err != nil {
		return in, err
	}
	var contextObject map[string]json.RawMessage
	if json.Unmarshal(in.Context, &contextObject) != nil || contextObject == nil {
		return in, fmt.Errorf("context must be object")
	}

	r, err := s.reducer(in.Root)
	if err != nil {
		return in, err
	}
	p, err := Canonical(in.Payload)
	if err != nil {
		return in, err
	}
	p, err = r.Normalize(in.Action, p)
	if err != nil {
		return in, err
	}
	in.Payload, err = Canonical(p)
	return in, err
}
func (s *Store) Append(ctx context.Context, inputs []Input) ([]Receipt, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("empty event batch")
	}
	normalized := make([]Input, len(inputs))
	fingerprints := make([]string, len(inputs))
	roots := map[string]Root{}
	for i, in := range inputs {
		v, e := s.normalize(in)
		if e != nil {
			return nil, errors.Join(ErrInvalid, e)
		}
		if e = validateInputSize(v); e != nil {
			return nil, e
		}
		normalized[i] = v
		fingerprints[i], e = requestFingerprint(v)
		if e != nil {
			return nil, e
		}
		roots[v.Root.Key()] = v.Root
	}
	ordered := make([]Root, 0, len(roots))
	for _, r := range roots {
		ordered = append(ordered, r)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key() < ordered[j].Key() })
	var receipts []Receipt
	err := s.Backend.Update(ctx, func(tx Tx) error {
		receipts = make([]Receipt, 0, len(inputs))
		states := map[string]Aggregate{}
		if e := tx.LockRoots(ctx, ordered); e != nil {
			return e
		}
		for i, in := range normalized {
			existing, e := tx.Find(ctx, in.Key)
			if e == nil {
				if existing.Fingerprint != fingerprints[i] {
					return ErrIdempotency
				}
				receipts = append(receipts, Receipt{Duplicate, existing})
				continue
			}
			if !errors.Is(e, ErrNotFound) {
				return e
			}
			n, e := tx.Count(ctx, in.Root)
			if e != nil {
				return e
			}
			if n >= 9007199254740991 {
				return fmt.Errorf("root counter exhausted")
			}
			if in.Expected != nil && *in.Expected != n {
				return ErrConflict
			}
			event := Event{Input: in, Number: n, Fingerprint: fingerprints[i]}
			event.Hash, e = Digest(event)
			if e != nil {
				return e
			}
			current, ok := states[in.Root.Key()]
			if !ok {
				current, e = s.aggregate(ctx, tx, in.Root)
				if e != nil {
					return e
				}
			}
			if current.Version != n-1 {
				return fmt.Errorf("root count disagrees with replay frontier")
			}
			reducer, e := s.reducer(in.Root)
			if e != nil {
				return e
			}
			next, e := applyReducer(reducer, current.State, event)
			if e != nil {
				return e
			}
			next, e = Canonical(next)
			if e != nil {
				return e
			}
			states[in.Root.Key()] = Aggregate{Root: in.Root, Version: n, State: next}
			if e = tx.PutCount(ctx, in.Root, n+1); e != nil {
				return e
			}
			if e = tx.PutEvent(ctx, event); e != nil {
				return e
			}
			if !s.Local {
				if e = tx.PutPublication(ctx, Publication{ID: event.Hash, Event: event}); e != nil {
					return e
				}
			}
			receipts = append(receipts, Receipt{Committed, event})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return receipts, nil
}

func Verify(e Event) error {
	h := e.Hash
	e.Hash = ""
	actual, err := Digest(e)
	if err != nil {
		return err
	}
	if h != actual {
		return fmt.Errorf("event hash mismatch")
	}
	f, err := requestFingerprint(e.Input)
	if err != nil {
		return err
	}
	if f != e.Fingerprint {
		return fmt.Errorf("event fingerprint mismatch")
	}
	return nil
}

type Aggregate struct {
	Root    Root            `json:"root"`
	Version int64           `json:"version"`
	State   json.RawMessage `json:"state"`
}

func (s *Store) aggregate(ctx context.Context, r Reader, root Root) (Aggregate, error) {
	a := Aggregate{root, -1, json.RawMessage(`{}`)}
	reducer, err := s.reducer(root)
	if err != nil {
		return a, err
	}
	snap, err := r.Snapshot(ctx, root)
	if err == nil {
		h := snap.Hash
		snap.Hash = ""
		actual, e := Digest(snap)
		if e != nil || h != actual {
			return a, fmt.Errorf("snapshot hash mismatch")
		}
		a.Version = snap.LastEvent
		a.State = snap.State
	} else if !errors.Is(err, ErrNotFound) {
		return a, err
	}
	err = r.Events(ctx, root, a.Version, func(e Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Number != a.Version+1 {
			return fmt.Errorf("event sequence gap")
		}
		if err := Verify(e); err != nil {
			return err
		}
		state, err := applyReducer(reducer, a.State, e)
		if err != nil {
			return err
		}
		a.State, err = Canonical(state)
		if err != nil {
			return err
		}
		a.Version = e.Number
		return nil
	})
	return a, err
}
func (s *Store) Aggregate(ctx context.Context, root Root) (Aggregate, error) {
	if validateRoot(root) != nil {
		return Aggregate{}, ErrInvalid
	}
	var a Aggregate
	err := s.Backend.View(ctx, func(r Reader) error { var e error; a, e = s.aggregate(ctx, r, root); return e })
	return a, err
}

// Deliver runs one bounded recovery pass. Publish success means provider
// acknowledgment, not subscriber completion. Delete failure permits redelivery.
func (s *Store) Deliver(ctx context.Context, p Publisher, limit int) error {
	if s.Local {
		return nil
	}
	if limit < 1 || limit > 10000 {
		return fmt.Errorf("invalid publication batch size")
	}
	if p == nil {
		return fmt.Errorf("publisher required")
	}
	var notices []Publication
	if err := s.Backend.View(ctx, func(r Reader) error { var e error; notices, e = r.Pending(ctx, limit); return e }); err != nil {
		return err
	}
	var failures []error
	for _, n := range notices {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(failures, err)...)
		}
		if e := p.Publish(ctx, n); e != nil {
			failures = append(failures, e)
			if e = s.Backend.Update(ctx, func(tx Tx) error { return tx.FailPublication(ctx, n.ID) }); e != nil {
				failures = append(failures, e)
			}
			continue
		}
		if e := s.Backend.Update(ctx, func(tx Tx) error { return tx.DeletePublication(ctx, n.ID) }); e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}
