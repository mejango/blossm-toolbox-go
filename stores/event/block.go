package eventstore

import (
	"context"
	"errors"
	"fmt"
)

type Signer interface {
	Sign(context.Context, []byte) ([]byte, error)
}

type FrontierReader interface {
	PendingRoots(context.Context, int) ([]Root, error)
}

// BuildBlock advances durable per-root snapshot frontiers atomically with the
// block. Wall-clock cutoffs never decide event membership.
func (s *Store) BuildBlock(ctx context.Context, maxRoots int, signer Signer) (Block, error) {
	var block Block
	if maxRoots < 1 {
		return block, fmt.Errorf("positive block root limit required")
	}
	err := s.Backend.Update(ctx, func(tx Tx) error {
		block = Block{}
		previous, e := tx.LastBlock(ctx)
		if e == nil {
			block.Number = previous.Number + 1
			block.Previous = previous.Hash
		} else if !errors.Is(e, ErrNotFound) {
			return e
		}
		var roots []Root
		if frontier, ok := tx.(FrontierReader); ok {
			roots, e = frontier.PendingRoots(ctx, maxRoots)
		} else {
			e = tx.Roots(ctx, func(r Root) error { roots = append(roots, r); return nil })
		}
		if e != nil {
			return e
		}
		var events []Event
		var snapshots []Snapshot
		var transactions []Transaction
		for _, root := range roots {
			if len(snapshots) == maxRoots {
				break
			}
			count, e := tx.Count(ctx, root)
			if e != nil {
				return e
			}
			old, e := tx.Snapshot(ctx, root)
			last := int64(-1)
			number := int64(0)
			if e == nil {
				last = old.LastEvent
				number = old.Number + 1
			} else if !errors.Is(e, ErrNotFound) {
				return e
			}
			if count <= last+1 {
				continue
			}
			var tail []Event
			if e = tx.Events(ctx, root, last, func(event Event) error { tail = append(tail, event); return nil }); e != nil {
				return e
			}
			a, e := s.aggregate(ctx, tx, root)
			if e != nil {
				return e
			}
			encoded, e := Encode(tail)
			if e != nil {
				return e
			}
			snapshot := Snapshot{Root: root, Number: number, LastEvent: a.Version, State: a.State, Events: encoded}
			snapshot.Hash, e = Digest(snapshot)
			if e != nil {
				return e
			}
			if e = tx.PutSnapshot(ctx, snapshot); e != nil {
				return e
			}
			events = append(events, tail...)
			snapshots = append(snapshots, snapshot)
			for _, event := range tail {
				transactions = append(transactions, event.Input.Transaction)
			}
		}
		block.Events, e = Encode(events)
		if e != nil {
			return e
		}
		block.Snapshots, e = Encode(snapshots)
		if e != nil {
			return e
		}
		block.Transactions, e = Encode(transactions)
		if e != nil {
			return e
		}
		block.Hash, e = Digest(block)
		if e != nil {
			return e
		}
		if signer != nil {
			block.Signature, e = signer.Sign(ctx, []byte(block.Hash))
			if e != nil {
				return e
			}
		}
		return tx.PutBlock(ctx, block)
	})
	return block, err
}

// Verifier must verify the configured producer identity; a self-supplied public
// key from an untrusted block is never an authority source.
type Verifier interface {
	Verify(context.Context, []byte, []byte) error
}

func VerifyBlock(ctx context.Context, block Block, previous *Block, verifier Verifier) error {
	if previous == nil {
		if block.Number != 0 || block.Previous != "" {
			return fmt.Errorf("invalid genesis block")
		}
	} else if block.Number != previous.Number+1 || block.Previous != previous.Hash {
		return fmt.Errorf("block chain mismatch")
	}
	hash, signature := block.Hash, block.Signature
	block.Hash = ""
	block.Signature = nil
	actual, err := Digest(block)
	if err != nil {
		return err
	}
	if actual != hash {
		return fmt.Errorf("block hash mismatch")
	}
	if verifier != nil {
		if len(signature) == 0 {
			return fmt.Errorf("block signature required")
		}
		if err = verifier.Verify(ctx, []byte(hash), signature); err != nil {
			return err
		}
	}
	return nil
}
