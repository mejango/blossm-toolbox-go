package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/evm"
)

// Journal commits immutable signed bytes and the account nonce frontier in one
// database transaction. Every process signing for this account must share it.
type Journal struct {
	Store   *eventstore.Store
	Network string
}
type nonceState struct {
	Next string `json:"next"`
}

func (j Journal) Get(ctx context.Context, key string) (evm.Prepared, error) {
	if j.Store == nil || j.Network == "" {
		return evm.Prepared{}, errors.New("journal store and network required")
	}
	a, e := j.Store.Aggregate(ctx, root(j.Network, "transaction", key))
	if e != nil {
		return evm.Prepared{}, e
	}
	if a.Version < 0 {
		return evm.Prepared{}, evm.ErrNotFound
	}
	var p evm.Prepared
	e = json.Unmarshal(a.State, &p)
	if e == nil && p.ID != key {
		e = errors.New("journal identity mismatch")
	}
	return p, e
}
func (j Journal) NextNonce(ctx context.Context, chain string, from common.Address, floor uint64) (uint64, error) {
	if j.Store == nil || j.Network == "" {
		return 0, errors.New("journal store and network required")
	}
	a, e := j.Store.Aggregate(ctx, root(j.Network, "nonce", id(chain, from.Hex())))
	if e != nil {
		return 0, e
	}
	if a.Version < 0 {
		return floor, nil
	}
	var n nonceState
	if e = json.Unmarshal(a.State, &n); e != nil {
		return 0, e
	}
	next, e := strconv.ParseUint(n.Next, 10, 64)
	if e != nil {
		return 0, e
	}
	if next < floor {
		return floor, nil
	}
	return next, nil
}
func (j Journal) Put(ctx context.Context, p evm.Prepared) error {
	if existing, e := j.Get(ctx, p.ID); e == nil {
		equal, err := samePrepared(existing, p)
		if err != nil {
			return err
		}
		if !equal {
			return evm.ErrConflict
		}
		return nil
	} else if !errors.Is(e, evm.ErrNotFound) {
		return e
	}
	if p.Nonce == math.MaxUint64 {
		return errors.New("nonce exhausted")
	}
	nroot := root(j.Network, "nonce", id(p.ChainID, p.From.Hex()))
	a, e := j.Store.Aggregate(ctx, nroot)
	if e != nil {
		return e
	}
	next, e := j.NextNonce(ctx, p.ChainID, p.From, p.Nonce)
	if e != nil {
		return e
	}
	if next > p.Nonce {
		return evm.ErrNonceConflict
	}
	_, e = j.Store.Append(ctx, []eventstore.Input{event(root(j.Network, "transaction", p.ID), "prepare", "wallet/tx/"+p.ID, 0, p), event(nroot, "reserve", fmt.Sprintf("wallet/nonce/%s/%d", nroot.ID, p.Nonce), a.Version+1, nonceState{fmt.Sprint(p.Nonce + 1)})})
	if errors.Is(e, eventstore.ErrConflict) || errors.Is(e, eventstore.ErrIdempotency) {
		if existing, readErr := j.Get(ctx, p.ID); readErr == nil {
			equal, err := samePrepared(existing, p)
			if err != nil {
				return err
			}
			if equal {
				return nil
			}
			return evm.ErrConflict
		} else if !errors.Is(readErr, evm.ErrNotFound) {
			return readErr
		}
		return evm.ErrNonceConflict
	}
	return e
}

func samePrepared(a, b evm.Prepared) (bool, error) {
	left, err := eventstore.Digest(a)
	if err != nil {
		return false, err
	}
	right, err := eventstore.Digest(b)
	if err != nil {
		return false, err
	}
	return left == right, nil
}
