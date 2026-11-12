package wallet

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/evm"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type fixtureRPC struct {
	pending     atomic.Uint64
	sends       atomic.Int32
	mutex       sync.Mutex
	onSend      func(*types.Transaction) error
	receipt     *types.Receipt
	block, head *types.Header
}

func (*fixtureRPC) ChainID(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (f *fixtureRPC) PendingNonceAt(context.Context, common.Address) (uint64, error) {
	return f.pending.Load(), nil
}
func (*fixtureRPC) EstimateGas(context.Context, ethereum.CallMsg) (uint64, error) { return 21000, nil }
func (f *fixtureRPC) SendTransaction(_ context.Context, tx *types.Transaction) error {
	f.sends.Add(1)
	f.mutex.Lock()
	fn := f.onSend
	f.mutex.Unlock()
	if fn != nil {
		return fn(tx)
	}
	return nil
}
func (f *fixtureRPC) TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.receipt == nil {
		return nil, ethereum.NotFound
	}
	copy := *f.receipt
	return &copy, nil
}
func (f *fixtureRPC) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if n == nil || (f.head != nil && f.head.Number.Cmp(n) == 0) {
		return f.head, nil
	}
	return f.block, nil
}
func (f *fixtureRPC) HeaderByHash(_ context.Context, h common.Hash) (*types.Header, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.block != nil && f.block.Hash() == h {
		return f.block, nil
	}
	return nil, ethereum.NotFound
}

func transfer(key string) evm.Request {
	return evm.Request{ID: key, To: common.HexToAddress("0x1111111111111111111111111111111111111111"), Value: big.NewInt(7), Gas: 21000, FeeCap: big.NewInt(2000000000), TipCap: big.NewInt(1000000000)}
}
func signedFixture(t *testing.T, key *ecdsa.PrivateKey, id string, nonce uint64) evm.Prepared {
	t.Helper()
	r := transfer(id)
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1), Nonce: nonce, To: &r.To, Value: r.Value, Gas: r.Gas, GasFeeCap: r.FeeCap, GasTipCap: r.TipCap})
	signed, err := types.SignTx(tx, types.LatestSignerForChainID(big.NewInt(1)), key)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := signed.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return evm.Prepared{ID: id, Intent: crypto.Keccak256Hash([]byte(id)), ChainID: "1", From: crypto.PubkeyToAddress(key.PublicKey), Nonce: nonce, Hash: signed.Hash(), Raw: raw}
}
func TestPrepareRequiresOriginalKeyBeforeHashing(t *testing.T) {
	s := Service{}
	for _, key := range []string{"", " \t\n", strings.Repeat("k", 257)} {
		in := transfer(key)
		if _, err := s.Prepare(context.Background(), "token", "wallet", "1", in); !errors.Is(err, dia.ErrInvalid) {
			t.Fatal("invalid original key became nonempty hash", err)
		}
	}
}
func TestTransactionReducerAndCanonicalNonce(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	defer key.D.SetInt64(0)
	p := signedFixture(t, key, "prepared", (1<<53)+1)
	raw, err := eventstore.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	r := reducer{domain: "transaction"}
	out, err := r.Normalize("prepare", raw)
	if err != nil {
		t.Fatal(err)
	}
	var recovered evm.Prepared
	if err = json.Unmarshal(out, &recovered); err != nil || recovered.Nonce != p.Nonce || !bytes.Equal(recovered.Raw, p.Raw) {
		t.Fatal(recovered, err)
	}
	ev := eventstore.Event{Input: eventstore.Input{Root: root("local", "transaction", "wrong-root"), Action: "prepare", Payload: raw}}
	if _, err = r.Apply(json.RawMessage(`{}`), ev); err == nil {
		t.Fatal("mismatched root identity accepted")
	}
	for _, change := range []func(*evm.Prepared){func(p *evm.Prepared) { p.Nonce++ }, func(p *evm.Prepared) { p.Intent = common.Hash{} }, func(p *evm.Prepared) { p.From[0] ^= 1 }, func(p *evm.Prepared) { p.ChainID = "10" }} {
		bad := p
		change(&bad)
		raw, err = eventstore.Encode(bad)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = r.Normalize("prepare", raw); err == nil {
			t.Fatal("invalid prepared transaction accepted")
		}
	}
}
func TestHTTPStrictIntegerAndKeyBoundary(t *testing.T) {
	server := httptest.NewServer(Service{}.Handler())
	defer server.Close()
	bodies := []string{
		`{"wallet":"w","chain":"1","to":"0x1111111111111111111111111111111111111111","value":"1","data":"0x","gas":21000,"feeCap":"2","tipCap":"1"}`,
		`{"wallet":"w","chain":"1","to":"0x1111111111111111111111111111111111111111","value":"1","data":"invalid","gas":"21000","feeCap":"2","tipCap":"1"}`,
	}
	for _, body := range bodies {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/commands/wallet/prepare", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Idempotency-Key", "key")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatal("invalid wire representation not rejected", response.StatusCode)
		}
	}
}
