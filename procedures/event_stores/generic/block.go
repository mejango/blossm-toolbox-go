package eventstore

import (
	"context"
	"errors"
	"fmt"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

// BuildBlock advances durable per-root snapshot frontiers atomically with the
// block. Wall-clock cutoffs never decide event membership.
func (s *Store) BuildBlock(ctx context.Context, maxRoots int, signer eventlib.Signer) (eventlib.Block, error) {
	var block eventlib.Block
	if maxRoots < 1 {
		return block, fmt.Errorf("positive block root limit required")
	}
	err := s.Backend.Update(ctx, func(tx eventlib.Tx) error {
		block = eventlib.Block{}
		previous, e := tx.LastBlock(ctx)
		if e == nil {
			block.Number = previous.Number + 1
			block.Previous = previous.Hash
		} else if !errors.Is(e, eventlib.ErrNotFound) {
			return e
		}
		var roots []eventlib.Root
		if frontier, ok := tx.(eventlib.FrontierReader); ok {
			roots, e = frontier.PendingRoots(ctx, maxRoots)
		} else {
			e = tx.Roots(ctx, func(r eventlib.Root) error { roots = append(roots, r); return nil })
		}
		if e != nil {
			return e
		}
		var events []eventlib.Event
		var snapshots []eventlib.Snapshot
		var transactions []eventlib.Transaction
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
			} else if !errors.Is(e, eventlib.ErrNotFound) {
				return e
			}
			if count <= last+1 {
				continue
			}
			var tail []eventlib.Event
			if e = tx.Events(ctx, root, last, func(event eventlib.Event) error { tail = append(tail, event); return nil }); e != nil {
				return e
			}
			a, e := s.aggregate(ctx, tx, root)
			if e != nil {
				return e
			}
			encoded, e := canonicaljson.Encode(tail)
			if e != nil {
				return e
			}
			snapshot := eventlib.Snapshot{Root: root, Number: number, LastEvent: a.Version, State: a.State, Events: encoded}
			snapshot.Hash, e = canonicaljson.Digest(snapshot)
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
		block.Events, e = canonicaljson.Encode(events)
		if e != nil {
			return e
		}
		block.Snapshots, e = canonicaljson.Encode(snapshots)
		if e != nil {
			return e
		}
		block.Transactions, e = canonicaljson.Encode(transactions)
		if e != nil {
			return e
		}
		block.Hash, e = canonicaljson.Digest(block)
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

func VerifyBlock(ctx context.Context, block eventlib.Block, previous *eventlib.Block, verifier eventlib.Verifier) error {
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
	actual, err := canonicaljson.Digest(block)
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
