package wallet

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"blossm.local/backend/eventstore"
	"blossm.local/backend/evm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type reducer struct{ domain string }

func (r reducer) Normalize(action string, raw json.RawMessage) (json.RawMessage, error) {
	switch r.domain {
	case "address":
		var w evm.Wallet
		if action != "create" {
			return nil, errors.New("unknown wallet action")
		}
		if e := eventstore.Decode(raw, &w); e != nil {
			return nil, e
		}
		if w.ID == "" || w.Owner == "" || w.Address == (common.Address{}) || len(w.EncryptedKey) < 16 {
			return nil, errors.New("invalid wallet")
		}
		return eventstore.Encode(w)
	case "transaction":
		var p evm.Prepared
		if action != "prepare" {
			return nil, errors.New("unknown transaction action")
		}
		if e := eventstore.Decode(raw, &p); e != nil {
			return nil, e
		}
		var tx types.Transaction
		if e := tx.UnmarshalBinary(p.Raw); e != nil {
			return nil, e
		}
		sender, e := types.Sender(types.LatestSignerForChainID(tx.ChainId()), &tx)
		if e != nil || p.ID == "" || p.Intent == (common.Hash{}) || tx.Type() != types.DynamicFeeTxType || tx.ChainId().Sign() <= 0 || tx.ChainId().BitLen() > 256 || tx.Hash() != p.Hash || tx.Nonce() != p.Nonce || sender != p.From || tx.ChainId().String() != p.ChainID {
			return nil, errors.New("invalid signed transaction")
		}
		return eventstore.Encode(p)
	case "nonce":
		var p nonceState
		if action != "reserve" {
			return nil, errors.New("unknown nonce action")
		}
		if e := eventstore.Decode(raw, &p); e != nil {
			return nil, e
		}
		n, e := strconv.ParseUint(p.Next, 10, 64)
		if e != nil || n == 0 || fmt.Sprint(n) != p.Next {
			return nil, errors.New("invalid nonce frontier")
		}
		return eventstore.Encode(p)
	case "observation":
		var p evm.Observation
		if action != "observe" {
			return nil, errors.New("unknown observation action")
		}
		if e := eventstore.Decode(raw, &p); e != nil {
			return nil, e
		}
		if p.ID == "" || p.Hash == (common.Hash{}) {
			return nil, errors.New("invalid observation")
		}
		switch p.State {
		case evm.Unknown, evm.Pending, evm.Succeeded, evm.Failed:
		default:
			return nil, errors.New("invalid observation state")
		}
		if (p.State == evm.Succeeded || p.State == evm.Failed) && (p.Confirmations == 0 || p.BlockHash == (common.Hash{})) {
			return nil, errors.New("terminal observation lacks evidence")
		}
		return eventstore.Encode(p)
	}
	return nil, errors.New("unknown wallet domain")
}
func (r reducer) Apply(state json.RawMessage, event eventstore.Event) (json.RawMessage, error) {
	if (r.domain == "address" || r.domain == "transaction") && event.Number != 0 {
		return nil, errors.New("immutable wallet record")
	}
	p, e := r.Normalize(event.Input.Action, event.Input.Payload)
	if e != nil {
		return nil, e
	}
	if r.domain == "address" || r.domain == "transaction" || r.domain == "observation" {
		var identity struct {
			ID string `json:"id"`
		}
		if e := json.Unmarshal(p, &identity); e != nil {
			return nil, e
		}
		if identity.ID != event.Input.Root.ID {
			return nil, errors.New("wallet record root identity mismatch")
		}
	}
	if r.domain == "nonce" {
		var old, next nonceState
		if e = json.Unmarshal(state, &old); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(p, &next); e != nil {
			return nil, e
		}
		n, _ := strconv.ParseUint(next.Next, 10, 64)
		before, _ := strconv.ParseUint(old.Next, 10, 64)
		if n <= before {
			return nil, errors.New("nonce frontier cannot retreat")
		}
	}
	return p, nil
}
func Reducers() map[string]eventstore.Reducer {
	m := map[string]eventstore.Reducer{}
	for _, d := range []string{"address", "transaction", "nonce", "observation"} {
		m["wallet/"+d] = reducer{d}
	}
	return m
}
