package evm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

var (
	ErrNotFound      = errors.New("transaction not found")
	ErrConflict      = errors.New("transaction ID already binds a different intent")
	ErrNonceConflict = errors.New("nonce already assigned")
)

// Client is implemented by *ethclient.Client. Only public inputs and signed bytes reach RPC.
type Client interface {
	ChainID(context.Context) (*big.Int, error)
	PendingNonceAt(context.Context, common.Address) (uint64, error)
	EstimateGas(context.Context, ethereum.CallMsg) (uint64, error)
	SendTransaction(context.Context, *types.Transaction) error
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	HeaderByHash(context.Context, common.Hash) (*types.Header, error)
}

// Repository stores immutable signed transactions. Put must durably commit before
// returning success, atomically enforcing unique ID and (chain, sender, nonce).
// Existing identical records succeed. NextNonce returns max(floor, stored nonce+1).
// Failed/unknown sends NEVER release nonces. All processes using an account must
// share this repository; externally signed transactions need a separate account.
type Repository interface {
	Get(context.Context, string) (Prepared, error)
	NextNonce(context.Context, string, common.Address, uint64) (uint64, error)
	Put(context.Context, Prepared) error
}

type Prepared struct {
	ID      string         `json:"id"`
	Intent  common.Hash    `json:"intent"`
	ChainID string         `json:"chainId"`
	From    common.Address `json:"from"`
	Nonce   uint64         `json:"nonce,string"`
	Hash    common.Hash    `json:"hash"`
	Raw     []byte         `json:"raw"`
}

// Request uses explicit EIP-1559 gas and fee caps so the caller authorizes the
// maximum spend (value + gas * feeCap). Amounts are integer wei; no floating point.
// Reusing ID with any changed request is rejected, including fee changes.
type Request struct {
	ID     string
	To     common.Address
	Value  *big.Int
	Data   []byte
	Gas    uint64
	FeeCap *big.Int
	TipCap *big.Int
}

type Engine struct {
	RPC     Client
	Keys    Cipher
	Journal Repository
	ChainID *big.Int
}

func (e Engine) checkChain(ctx context.Context) error {
	if e.RPC == nil || e.ChainID == nil || e.ChainID.Sign() <= 0 || e.ChainID.BitLen() > 256 {
		return errors.New("invalid chain configuration")
	}
	actual, err := e.RPC.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("read chain: %w", err)
	}
	if actual == nil || actual.Cmp(e.ChainID) != 0 {
		return errors.New("RPC chain mismatch")
	}
	return nil
}

func uint256(v *big.Int) bool { return v != nil && v.Sign() >= 0 && v.BitLen() <= 256 }

func (e Engine) Prepare(ctx context.Context, w Wallet, req Request) (Prepared, error) {
	if e.Journal == nil || req.ID == "" || req.To == (common.Address{}) || !uint256(req.Value) || !uint256(req.FeeCap) || !uint256(req.TipCap) || req.Gas < 21000 || req.FeeCap.Sign() == 0 || req.TipCap.Cmp(req.FeeCap) > 0 {
		return Prepared{}, errors.New("invalid transaction request")
	}
	if err := e.checkChain(ctx); err != nil {
		return Prepared{}, err
	}
	intentBytes, _ := json.Marshal(struct {
		Chain    string
		WalletID string
		Owner    string
		From     common.Address
		Request  Request
	}{e.ChainID.String(), w.ID, w.Owner, w.Address, req})
	intent := crypto.Keccak256Hash(intentBytes)
	if p, err := e.Journal.Get(ctx, req.ID); err == nil {
		if p.Intent != intent {
			return Prepared{}, ErrConflict
		}
		_, err = e.validate(p)
		return p, err
	} else if !errors.Is(err, ErrNotFound) {
		return Prepared{}, err
	}
	estimated, err := e.RPC.EstimateGas(ctx, ethereum.CallMsg{From: w.Address, To: &req.To, Gas: req.Gas, GasFeeCap: req.FeeCap, GasTipCap: req.TipCap, Value: req.Value, Data: req.Data})
	if err != nil {
		return Prepared{}, fmt.Errorf("transaction gas preflight: %w", err)
	}
	if estimated < 21000 || estimated > req.Gas {
		return Prepared{}, errors.New("gas preflight exceeds approved limit or is invalid")
	}
	key, err := openKey(ctx, e.Keys, w)
	if err != nil {
		return Prepared{}, err
	}
	defer clearPrivateKey(key)
	pending, err := e.RPC.PendingNonceAt(ctx, w.Address)
	if err != nil {
		return Prepared{}, fmt.Errorf("read pending nonce: %w", err)
	}
	// Concurrent prepares can race between selection and atomic Put. Retry boundedly.
	for attempt := 0; attempt < 8; attempt++ {
		nonce, err := e.Journal.NextNonce(ctx, e.ChainID.String(), w.Address, pending)
		if err != nil {
			return Prepared{}, err
		}
		if nonce == math.MaxUint64 {
			return Prepared{}, errors.New("nonce exhausted")
		}
		tx := types.NewTx(&types.DynamicFeeTx{ChainID: new(big.Int).Set(e.ChainID), Nonce: nonce, To: &req.To, Value: new(big.Int).Set(req.Value), Data: append([]byte(nil), req.Data...), Gas: req.Gas, GasFeeCap: new(big.Int).Set(req.FeeCap), GasTipCap: new(big.Int).Set(req.TipCap)})
		signed, err := types.SignTx(tx, types.LatestSignerForChainID(e.ChainID), key)
		if err != nil {
			return Prepared{}, errors.New("local transaction signing failed")
		}
		raw, err := signed.MarshalBinary()
		if err != nil {
			return Prepared{}, err
		}
		p := Prepared{ID: req.ID, Intent: intent, ChainID: e.ChainID.String(), From: w.Address, Nonce: nonce, Hash: signed.Hash(), Raw: raw}
		err = e.Journal.Put(ctx, p)
		if err == nil {
			return p, nil
		}
		if errors.Is(err, ErrConflict) {
			old, readErr := e.Journal.Get(ctx, req.ID)
			if readErr != nil {
				return Prepared{}, readErr
			}
			if old.Intent != intent {
				return Prepared{}, ErrConflict
			}
			_, readErr = e.validate(old)
			return old, readErr
		}
		if !errors.Is(err, ErrNonceConflict) {
			return Prepared{}, err
		}
	}
	return Prepared{}, ErrNonceConflict
}

func (e Engine) validate(p Prepared) (*types.Transaction, error) {
	if e.ChainID == nil || p.ChainID != e.ChainID.String() {
		return nil, errors.New("stored chain mismatch")
	}
	var tx types.Transaction
	if err := tx.UnmarshalBinary(p.Raw); err != nil {
		return nil, errors.New("invalid stored transaction")
	}
	if tx.Type() != types.DynamicFeeTxType || tx.ChainId().Cmp(e.ChainID) != 0 || tx.Hash() != p.Hash || tx.Nonce() != p.Nonce {
		return nil, errors.New("stored transaction binding mismatch")
	}
	sender, err := types.Sender(types.LatestSignerForChainID(e.ChainID), &tx)
	if err != nil || sender != p.From {
		return nil, errors.New("stored transaction sender mismatch")
	}
	return &tx, nil
}

type State string

const (
	Unknown   State = "unknown"
	Pending   State = "pending"
	Succeeded State = "succeeded"
	Failed    State = "failed"
)

// Observation contains chain evidence, not wall-clock-derived replay state.
// Failed means a canonically included, sufficiently confirmed receipt with status 0.
type Observation struct {
	ID            string
	Hash          common.Hash
	State         State
	BlockNumber   uint64 `json:"blockNumber,string"`
	BlockHash     common.Hash
	Confirmations uint64 `json:"confirmations,string"`
}

// Submit loads only durable bytes; no signing or nonce allocation happens here.
// Any send error is ambiguous. Retrying this ID broadcasts the exact same hash.
func (e Engine) Submit(ctx context.Context, id string) (Observation, error) {
	o := Observation{ID: id, State: Unknown}
	if e.Journal == nil {
		return o, errors.New("journal required")
	}
	p, err := e.Journal.Get(ctx, id)
	if err != nil {
		return o, err
	}
	o.Hash = p.Hash
	tx, err := e.validate(p)
	if err != nil {
		return o, err
	}
	if err = e.checkChain(ctx); err != nil {
		return o, err
	}
	if err = e.RPC.SendTransaction(ctx, tx); err != nil {
		return o, fmt.Errorf("submission outcome unknown: %w", err)
	}
	o.State = Pending
	return o, nil
}

type PollPolicy struct {
	Attempts      int
	Interval      time.Duration
	Timeout       time.Duration // total bound, including RPC calls
	Confirmations uint64        `json:"confirmations,string"`
}

// Reconcile bounds the entire observation budget and checks canonical block hash.
// No receipt (including a dropped/replaced transaction) remains unknown, never failed.
func (e Engine) Reconcile(ctx context.Context, id string, policy PollPolicy) (Observation, error) {
	o := Observation{ID: id, State: Unknown}
	if policy.Attempts < 1 || policy.Attempts > 100 || policy.Timeout <= 0 || policy.Interval < 0 || policy.Confirmations < 1 || e.Journal == nil {
		return o, errors.New("invalid reconciliation policy")
	}
	ctx, cancel := context.WithTimeout(ctx, policy.Timeout)
	defer cancel()
	p, err := e.Journal.Get(ctx, id)
	if err != nil {
		return o, err
	}
	o.Hash = p.Hash
	if _, err = e.validate(p); err != nil {
		return o, err
	}
	if err = e.checkChain(ctx); err != nil {
		return o, err
	}
	var last error
	for i := 0; i < policy.Attempts; i++ {
		// Reset stale observations if the next poll observes a reorg or unavailable RPC.
		o = Observation{ID: id, Hash: p.Hash, State: Unknown}
		receipt, readErr := e.RPC.TransactionReceipt(ctx, p.Hash)
		last = readErr
		if readErr == nil && receipt != nil {
			if receipt.TxHash != p.Hash || receipt.BlockNumber == nil || !receipt.BlockNumber.IsUint64() || receipt.BlockHash == (common.Hash{}) || receipt.Status > 1 {
				return o, errors.New("invalid transaction receipt")
			}
			confirmations, confirmErr := e.confirmations(ctx, receipt, policy.Confirmations)
			last = confirmErr
			if confirmErr == nil && confirmations > 0 {
				o.BlockNumber = receipt.BlockNumber.Uint64()
				o.BlockHash = receipt.BlockHash
				o.Confirmations = confirmations
				o.State = Pending
				if confirmations >= policy.Confirmations {
					o.State = Succeeded
					if receipt.Status == types.ReceiptStatusFailed {
						o.State = Failed
					}
					return o, nil
				}
			}
		} else if errors.Is(readErr, ethereum.NotFound) {
			last = nil
		}
		if i+1 < policy.Attempts {
			timer := time.NewTimer(policy.Interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return o, ctx.Err()
			case <-timer.C:
			}
		}
	}
	if ctx.Err() != nil {
		return o, ctx.Err()
	}
	return o, last
}

// confirmations proves ancestry from a canonical anchor at no more than the
// requested threshold. It never mixes a receipt block with an unrelated head.
// The total reconciliation context bounds this walk as well as polling.
func (e Engine) confirmations(ctx context.Context, receipt *types.Receipt, required uint64) (uint64, error) {
	head, err := e.RPC.HeaderByNumber(ctx, nil)
	if err != nil {
		return 0, err
	}
	if head == nil || head.Number == nil || !head.Number.IsUint64() || head.Number.Cmp(receipt.BlockNumber) < 0 {
		return 0, nil
	}
	bottom := receipt.BlockNumber.Uint64()
	top := head.Number.Uint64()
	if required-1 > math.MaxUint64-bottom {
		return 0, errors.New("confirmation height overflow")
	}
	target := bottom + required - 1
	if top > target {
		head, err = e.RPC.HeaderByNumber(ctx, new(big.Int).SetUint64(target))
		if err != nil {
			return 0, err
		}
		if head == nil || head.Number == nil || !head.Number.IsUint64() || head.Number.Uint64() != target {
			return 0, errors.New("invalid confirmation anchor")
		}
		top = target
	}
	anchorHash := head.Hash()
	for number := top; number > bottom; number-- {
		if err = ctx.Err(); err != nil {
			return 0, err
		}
		parentHash := head.ParentHash
		head, err = e.RPC.HeaderByHash(ctx, parentHash)
		if err != nil {
			return 0, err
		}
		if head == nil || head.Number == nil || !head.Number.IsUint64() || head.Number.Uint64() != number-1 || head.Hash() != parentHash {
			return 0, errors.New("invalid confirmation ancestry")
		}
	}
	if head.Hash() != receipt.BlockHash {
		return 0, nil
	}
	canonical, err := e.RPC.HeaderByNumber(ctx, new(big.Int).SetUint64(top))
	if err != nil {
		return 0, err
	}
	if canonical == nil || canonical.Number == nil || !canonical.Number.IsUint64() || canonical.Number.Uint64() != top || canonical.Hash() != anchorHash {
		return 0, nil
	}
	return top - bottom + 1, nil
}
