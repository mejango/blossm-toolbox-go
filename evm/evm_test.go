package evm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type fakeClient struct {
	chain        *big.Int
	pending      uint64
	send         func(*types.Transaction) error
	receipt      *types.Receipt
	receiptErr   error
	block        *types.Header
	head         *types.Header
	receiptCalls int
	blockReceipt bool
	estimateErr  error
}

func (f *fakeClient) ChainID(context.Context) (*big.Int, error) { return f.chain, nil }
func (f *fakeClient) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	return f.pending, nil
}
func (f *fakeClient) EstimateGas(_ context.Context, msg ethereum.CallMsg) (uint64, error) {
	if f.estimateErr != nil {
		return 0, f.estimateErr
	}
	gas := uint64(21000)
	for _, b := range msg.Data {
		if b == 0 {
			gas += 4
		} else {
			gas += 16
		}
	}
	return gas, nil
}
func (f *fakeClient) HeaderByHash(_ context.Context, hash common.Hash) (*types.Header, error) {
	if f.block != nil && f.block.Hash() == hash {
		return f.block, nil
	}
	return nil, ethereum.NotFound
}
func (f *fakeClient) SendTransaction(_ context.Context, tx *types.Transaction) error {
	if f.send != nil {
		return f.send(tx)
	}
	return nil
}
func (f *fakeClient) TransactionReceipt(ctx context.Context, _ common.Hash) (*types.Receipt, error) {
	f.receiptCalls++
	if f.blockReceipt {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.receipt == nil && f.receiptErr == nil {
		return nil, ethereum.NotFound
	}
	return f.receipt, f.receiptErr
}
func (f *fakeClient) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	if n == nil {
		return f.head, nil
	}
	if f.head != nil && f.head.Number.Cmp(n) == 0 {
		return f.head, nil
	}
	return f.block, nil
}

func setup(t *testing.T) (Engine, Wallet, *fakeClient) {
	t.Helper()
	c := newCipher(t)
	w, err := CreateWallet(context.Background(), c, "wallet-1", "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeClient{chain: big.NewInt(1), pending: 7}
	e := Engine{RPC: f, Keys: c, Journal: FileRepository{Directory: t.TempDir()}, ChainID: big.NewInt(1)}
	return e, w, f
}
func request(id string) Request {
	return Request{ID: id, To: common.HexToAddress("0x1111111111111111111111111111111111111111"), Value: big.NewInt(123), Gas: 21000, FeeCap: big.NewInt(2000000000), TipCap: big.NewInt(1000000000)}
}
func mustPrepare(t *testing.T, e Engine, w Wallet, id string) Prepared {
	t.Helper()
	p, err := e.Prepare(context.Background(), w, request(id))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCustodyAuthenticatedBindings(t *testing.T) {
	e, w, _ := setup(t)
	ctx := context.Background()
	key, err := openKey(ctx, e.Keys, w)
	if err != nil {
		t.Fatal(err)
	}
	raw := crypto.FromECDSA(key)
	defer clear(raw)
	defer key.D.SetInt64(0)
	if bytes.Contains(w.EncryptedKey, raw) {
		t.Fatal("plaintext in wallet")
	}
	for _, mutate := range []func(*Wallet){func(w *Wallet) { w.Owner = "attacker" }, func(w *Wallet) { w.ID = "other" }, func(w *Wallet) { w.Address[0] ^= 1 }, func(w *Wallet) { w.EncryptedKey = append([]byte(nil), w.EncryptedKey...); w.EncryptedKey[20] ^= 1 }} {
		other := w
		mutate(&other)
		if _, err := openKey(ctx, e.Keys, other); err == nil {
			t.Fatal("tampering accepted")
		}
	}
}

func TestDurableBeforeSubmitAndUncertainRetry(t *testing.T) {
	ctx := context.Background()
	e, w, f := setup(t)
	p := mustPrepare(t, e, w, "tx-1")
	var sends int
	f.send = func(tx *types.Transaction) error {
		sends++
		// Open a fresh repository instance as if the process restarted.
		disk := FileRepository{Directory: e.Journal.(FileRepository).Directory}
		saved, err := disk.Get(ctx, "tx-1")
		if err != nil || saved.Hash != tx.Hash() {
			t.Fatal("send preceded persistence")
		}
		encoded, _ := tx.MarshalBinary()
		if !bytes.Equal(saved.Raw, encoded) {
			t.Fatal("signed bytes changed")
		}
		sender, err := types.Sender(types.LatestSignerForChainID(big.NewInt(1)), tx)
		if err != nil || sender != w.Address {
			t.Fatal("wrong signature")
		}
		if sends == 1 {
			return errors.New("connection reset after acceptance")
		}
		return nil
	}
	o, err := e.Submit(ctx, p.ID)
	if err == nil || o.State != Unknown || o.Hash != p.Hash {
		t.Fatalf("uncertain send: %+v %v", o, err)
	}
	e.Keys = nil // recovery cannot need keys or re-sign
	e.Journal = FileRepository{Directory: e.Journal.(FileRepository).Directory}
	o, err = e.Submit(ctx, p.ID)
	if err != nil || o.State != Pending || sends != 2 {
		t.Fatalf("retry: %+v %v", o, err)
	}
	again := mustPrepare(t, e, w, p.ID)
	if !bytes.Equal(again.Raw, p.Raw) {
		t.Fatal("idempotent prepare changed bytes")
	}
	changed := request(p.ID)
	changed.Value = big.NewInt(999)
	if _, err = e.Prepare(ctx, w, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("intent conflict not rejected", err)
	}
	blob, err := os.ReadFile(filepath.Join(e.Journal.(FileRepository).Directory, "transactions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, w.EncryptedKey) || bytes.Contains(blob, []byte("encryptedKey")) {
		t.Fatal("custody leaked to journal")
	}
}

type failedRepository struct{ Repository }

func (r failedRepository) Put(context.Context, Prepared) error { return errors.New("disk full") }
func TestPersistenceFailureCannotSubmit(t *testing.T) {
	e, w, f := setup(t)
	e.Journal = failedRepository{e.Journal}
	f.send = func(*types.Transaction) error { t.Fatal("sent without persistence"); return nil }
	if _, err := e.Prepare(context.Background(), w, request("no-disk")); err == nil {
		t.Fatal("ignored persistence failure")
	}
	if _, err := e.Submit(context.Background(), "no-disk"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestConcurrentNoncePolicyAndRestart(t *testing.T) {
	e, w, f := setup(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan Prepared, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := e.Prepare(ctx, w, request(fmt.Sprintf("tx-%d", i)))
			results <- p
			errs <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	nonces := map[uint64]bool{}
	for p := range results {
		if nonces[p.Nonce] {
			t.Fatal("nonce reused")
		}
		nonces[p.Nonce] = true
	}
	for n := uint64(7); n < 11; n++ {
		if !nonces[n] {
			t.Fatal("nonce gap", nonces)
		}
	}
	f.pending = 0 // stale endpoint cannot reset durable allocation
	if p := mustPrepare(t, e, w, "after-restart"); p.Nonce != 11 {
		t.Fatal(p.Nonce)
	}
	f.pending = 20 // respect higher externally observed pending nonce
	if p := mustPrepare(t, e, w, "after-pending"); p.Nonce != 20 {
		t.Fatal(p.Nonce)
	}
}

func TestTransactionValidationAndChainBinding(t *testing.T) {
	e, w, f := setup(t)
	ctx := context.Background()
	for _, mutate := range []func(*Request){func(r *Request) { r.Value = big.NewInt(-1) }, func(r *Request) { r.FeeCap = nil }, func(r *Request) { r.Gas = 1 }, func(r *Request) { r.TipCap = big.NewInt(3000000000) }, func(r *Request) { r.To = common.Address{} }, func(r *Request) { r.Value = new(big.Int).Lsh(big.NewInt(1), 256) }} {
		r := request("bad")
		mutate(&r)
		if _, err := e.Prepare(ctx, w, r); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	p := mustPrepare(t, e, w, "good")
	f.chain = big.NewInt(10)
	if _, err := e.Submit(ctx, p.ID); err == nil {
		t.Fatal("wrong chain submission")
	}
	if _, err := e.Prepare(ctx, w, request("wrong-chain")); err == nil {
		t.Fatal("wrong chain prepare")
	}
	for _, mutate := range []func(*Prepared){func(p *Prepared) { p.Hash[0] ^= 1 }, func(p *Prepared) { p.From[0] ^= 1 }, func(p *Prepared) { p.Nonce++ }, func(p *Prepared) { p.ChainID = "10" }, func(p *Prepared) { p.Raw = []byte{1, 2} }} {
		bad := p
		mutate(&bad)
		if _, err := e.validate(bad); err == nil {
			t.Fatal("bad journal binding accepted")
		}
	}
}

func TestBoundedReceiptReconciliation(t *testing.T) {
	e, w, f := setup(t)
	ctx := context.Background()
	p := mustPrepare(t, e, w, "receipt")
	policy := PollPolicy{Attempts: 3, Timeout: time.Second, Confirmations: 2}
	o, err := e.Reconcile(ctx, p.ID, policy)
	if err != nil || o.State != Unknown || f.receiptCalls != 3 {
		t.Fatalf("missing: %+v %v calls %d", o, err, f.receiptCalls)
	}
	f.receiptErr = errors.New("RPC unavailable")
	o, err = e.Reconcile(ctx, p.ID, policy)
	if err == nil || o.State != Unknown {
		t.Fatal("transport error became terminal")
	}
	f.receiptErr = nil
	f.block = &types.Header{Number: big.NewInt(100), Time: 1000}
	f.head = &types.Header{Number: big.NewInt(100), Time: 1000}
	f.receipt = &types.Receipt{TxHash: p.Hash, BlockNumber: big.NewInt(100), BlockHash: f.block.Hash(), Status: 1}
	o, err = e.Reconcile(ctx, p.ID, policy)
	if err != nil || o.State != Pending {
		t.Fatalf("unconfirmed: %+v %v", o, err)
	}
	f.head = &types.Header{Number: big.NewInt(101), Time: 1012, ParentHash: f.block.Hash()}
	o, err = e.Reconcile(ctx, p.ID, policy)
	if err != nil || o.State != Succeeded || o.Confirmations != 2 {
		t.Fatalf("success: %+v %v", o, err)
	}
	replay, err := e.Reconcile(ctx, p.ID, policy)
	if err != nil || replay != o {
		t.Fatal("nondeterministic evidence replay")
	}
	f.receipt.Status = 0
	o, err = e.Reconcile(ctx, p.ID, policy)
	if err != nil || o.State != Failed {
		t.Fatal("revert not recognized")
	}
	f.block.Extra = []byte("reorg")
	o, err = e.Reconcile(ctx, p.ID, policy)
	if o.State != Unknown {
		t.Fatal("noncanonical receipt accepted")
	}
	f.blockReceipt = true
	policy.Timeout = 10 * time.Millisecond
	start := time.Now()
	o, err = e.Reconcile(ctx, p.ID, policy)
	if !errors.Is(err, context.DeadlineExceeded) || o.State != Unknown || time.Since(start) > time.Second {
		t.Fatalf("budget: %+v %v", o, err)
	}
}

// This fixture crosses the actual go-ethereum RPC codec and ethclient, in process.
// The only submitted RPC material is a serialized signed transaction.
type rpcFixture struct {
	raw   []byte
	calls []string
}

func (f *rpcFixture) ChainId() *hexutil.Big {
	f.calls = append(f.calls, "eth_chainId")
	return (*hexutil.Big)(big.NewInt(1))
}
func (f *rpcFixture) GetTransactionCount(_ context.Context, _ common.Address, tag string) (hexutil.Uint64, error) {
	f.calls = append(f.calls, "eth_getTransactionCount")
	if tag != "pending" {
		return 0, errors.New("must use pending")
	}
	return 4, nil
}
func (f *rpcFixture) EstimateGas(args map[string]any) (hexutil.Uint64, error) {
	f.calls = append(f.calls, "eth_estimateGas")
	if args["from"] == nil || args["to"] == nil || args["gas"] == nil {
		return 0, errors.New("missing estimate fields")
	}
	return 21000, nil
}
func (f *rpcFixture) SendRawTransaction(raw hexutil.Bytes) (common.Hash, error) {
	f.calls = append(f.calls, "eth_sendRawTransaction")
	f.raw = append([]byte(nil), raw...)
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}
	return tx.Hash(), nil
}
func TestActualEthclientRPC(t *testing.T) {
	server := rpc.NewServer()
	defer server.Stop()
	fixture := &rpcFixture{}
	if err := server.RegisterName("eth", fixture); err != nil {
		t.Fatal(err)
	}
	transport := rpc.DialInProc(server)
	defer transport.Close()
	client := ethclient.NewClient(transport)
	e, w, _ := setup(t)
	e.RPC = client
	p := mustPrepare(t, e, w, "rpc")
	if _, err := e.Submit(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(fixture.raw, p.Raw) || p.Nonce != 4 {
		t.Fatal("RPC transaction mismatch")
	}
	for _, method := range fixture.calls {
		if strings.Contains(method, "sign") || strings.Contains(method, "personal") {
			t.Fatal("remote signing")
		}
	}
}

func TestJournalUniquenessCorruptionAndCancelledAccess(t *testing.T) {
	e, w, _ := setup(t)
	ctx := context.Background()
	p := mustPrepare(t, e, w, "first")
	if err := e.Journal.Put(ctx, p); err != nil {
		t.Fatal("exact duplicate must succeed", err)
	}
	second := e
	second.Journal = FileRepository{Directory: t.TempDir()}
	other := mustPrepare(t, second, w, "second")
	if err := e.Journal.Put(ctx, other); !errors.Is(err, ErrNonceConflict) {
		t.Fatal("nonce uniqueness", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := e.Journal.Get(cancelled, p.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation", err)
	}
	path := filepath.Join(e.Journal.(FileRepository).Directory, "transactions.json")
	if err := os.WriteFile(path, []byte(`{"first":{"id":"first","chainId":"1"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Journal.NextNonce(ctx, "1", w.Address, 0); err == nil {
		t.Fatal("corrupt nonce state accepted")
	}
}

type leakingCipher struct{}

func (leakingCipher) Seal(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errors.New("secret private key")
}
func (leakingCipher) Open(context.Context, []byte, []byte) ([]byte, error) {
	return nil, errors.New("secret private key")
}
func TestCustodyErrorsAreRedacted(t *testing.T) {
	_, err := CreateWallet(context.Background(), leakingCipher{}, "id", "owner")
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("encryption secret leaked")
	}
	_, err = openKey(context.Background(), leakingCipher{}, Wallet{ID: "id", Owner: "owner", EncryptedKey: []byte{1}})
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatal("decryption secret leaked")
	}
}

func TestGasPreflightDoesNotReserveInvalidTransaction(t *testing.T) {
	e, w, f := setup(t)
	ctx := context.Background()
	r := request("gas-invalid")
	r.Data = []byte{1}
	if _, err := e.Prepare(ctx, w, r); err == nil {
		t.Fatal("insufficient intrinsic gas accepted")
	}
	if _, err := e.Journal.Get(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("invalid transaction reserved nonce")
	}
	f.estimateErr = errors.New("execution reverted")
	if _, err := e.Prepare(ctx, w, request("reverts")); err == nil {
		t.Fatal("failed simulation ignored")
	}
	f.estimateErr = nil
	if p := mustPrepare(t, e, w, "valid-after-preflight"); p.Nonce != 7 {
		t.Fatal("preflight failure burned nonce")
	}
}

func TestConfirmationHistoryMustContainReceipt(t *testing.T) {
	e, w, f := setup(t)
	p := mustPrepare(t, e, w, "ancestry")
	old := &types.Header{Number: big.NewInt(100), Extra: []byte("old")}
	f.block = &types.Header{Number: big.NewInt(100), Extra: []byte("new")}
	f.head = &types.Header{Number: big.NewInt(101), ParentHash: f.block.Hash()}
	f.receipt = &types.Receipt{TxHash: p.Hash, BlockNumber: big.NewInt(100), BlockHash: old.Hash(), Status: 1}
	o, err := e.Reconcile(context.Background(), p.ID, PollPolicy{Attempts: 1, Timeout: time.Second, Confirmations: 2})
	if err != nil || o.State != Unknown {
		t.Fatalf("mixed histories accepted: %+v %v", o, err)
	}
}
