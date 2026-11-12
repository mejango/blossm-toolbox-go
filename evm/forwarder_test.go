package evm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

type forwardFixture struct {
	t           *testing.T
	wallet      common.Address
	chain       *big.Int
	domainChain *big.Int
	verifier    common.Address
	block       *types.Header
	nonce       *big.Int
	fields      [1]byte
	extensions  []*big.Int
	calls       int
	malformed   bool
}

func (f *forwardFixture) ChainID(context.Context) (*big.Int, error) { return f.chain, nil }
func (f *forwardFixture) HeaderByNumber(context.Context, *big.Int) (*types.Header, error) {
	return f.block, nil
}
func (f *forwardFixture) CallContractAtHash(_ context.Context, msg ethereum.CallMsg, hash common.Hash) ([]byte, error) {
	f.calls++
	expected, _ := ForwarderAddress(big.NewInt(1))
	if msg.To == nil || *msg.To != expected || hash != f.block.Hash() {
		f.t.Fatal("unbound RPC read")
	}
	if f.malformed {
		return []byte{1, 2}, nil
	}
	contract, err := ForwarderABI()
	if err != nil {
		f.t.Fatal(err)
	}
	if bytes.Equal(msg.Data, contract.Methods["eip712Domain"].ID) {
		return contract.Methods["eip712Domain"].Outputs.Pack(f.fields, "Juicebox", "1", f.domainChain, f.verifier, [32]byte{}, f.extensions)
	}
	nonceData, err := contract.Pack("nonces", f.wallet)
	if err != nil {
		f.t.Fatal(err)
	}
	if !bytes.Equal(msg.Data, nonceData) {
		f.t.Fatal("nonce queried for wrong sender")
	}
	return contract.Methods["nonces"].Outputs.Pack(f.nonce)
}
func forwardSetup(t *testing.T) (Engine, Wallet, *forwardFixture, ForwardRequest) {
	e, w, _ := setup(t)
	address, _ := ForwarderAddress(big.NewInt(1))
	f := &forwardFixture{t: t, wallet: w.Address, chain: big.NewInt(1), domainChain: big.NewInt(1), verifier: address, block: &types.Header{Number: big.NewInt(10), Time: 1000}, nonce: big.NewInt(42), fields: [1]byte{15}, extensions: []*big.Int{}}
	req := ForwardRequest{To: request("unused").To, Value: big.NewInt(7), Gas: big.NewInt(100000), Deadline: 2000, Data: []byte{1, 2, 3, 4}}
	return e, w, f, req
}

// Independent EIP-712 vector construction: ABI struct encoding and 0x1901
// concatenation, rather than the apitypes implementation exercised by production.
func manualDigest(t *testing.T, w Wallet, f *forwardFixture, r ForwardRequest) common.Hash {
	t.Helper()
	pack := func(names []string, values ...any) []byte {
		args := make(abi.Arguments, len(names))
		for i, name := range names {
			typ, err := abi.NewType(name, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			args[i] = abi.Argument{Type: typ}
		}
		out, err := args.Pack(values...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	domain := crypto.Keccak256Hash(pack([]string{"bytes32", "bytes32", "bytes32", "uint256", "address"}, crypto.Keccak256Hash([]byte("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)")), crypto.Keccak256Hash([]byte("Juicebox")), crypto.Keccak256Hash([]byte("1")), f.domainChain, f.verifier))
	message := crypto.Keccak256Hash(pack([]string{"bytes32", "address", "address", "uint256", "uint256", "uint256", "uint48", "bytes32"}, crypto.Keccak256Hash([]byte("ForwardRequest(address from,address to,uint256 value,uint256 gas,uint256 nonce,uint48 deadline,bytes data)")), w.Address, r.To, r.Value, r.Gas, f.nonce, new(big.Int).SetUint64(r.Deadline), crypto.Keccak256Hash(r.Data)))
	return crypto.Keccak256Hash([]byte{0x19, 0x01}, domain[:], message[:])
}

func TestForwardRequestRealSignatureAndABI(t *testing.T) {
	e, w, f, req := forwardSetup(t)
	call, err := SignForwardRequest(context.Background(), f, e.Keys, w, big.NewInt(1), req)
	if err != nil {
		t.Fatal(err)
	}
	if call.Digest != manualDigest(t, w, f, req) || call.Nonce.Cmp(big.NewInt(42)) != 0 || f.calls != 2 {
		t.Fatal("typed digest/domain/nonce mismatch")
	}
	signature := append([]byte(nil), call.Signature...)
	signature[64] -= 27
	public, err := crypto.SigToPub(call.Digest[:], signature)
	if err != nil || crypto.PubkeyToAddress(*public) != w.Address {
		t.Fatal("cannot recover custodial signer", err)
	}
	contract, err := ForwarderABI()
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(call.Data[:4]) != "df905caf" {
		t.Fatalf("unexpected execute selector: %x", call.Data[:4])
	}
	unpacked, err := contract.Methods["execute"].Inputs.Unpack(call.Data[4:])
	if err != nil {
		t.Fatal(err)
	}
	repacked, err := contract.Pack("execute", unpacked...)
	if err != nil || !bytes.Equal(call.Data, repacked) {
		t.Fatal("execute encoding cannot round trip", err)
	}
	f.nonce = big.NewInt(43)
	next, err := SignForwardRequest(context.Background(), f, e.Keys, w, big.NewInt(1), req)
	if err != nil || next.Digest == call.Digest || bytes.Equal(next.Signature, call.Signature) {
		t.Fatal("nonce replay binding absent", err)
	}
	// Forward execute calldata is usable by the same durable direct transaction flow.
	direct := request("forward-submit")
	direct.To = call.To
	direct.Value = call.Value
	direct.Data = call.Data
	direct.Gas = 200000
	if _, err = e.Prepare(context.Background(), w, direct); err != nil {
		t.Fatal(err)
	}
}

func TestForwardBindingRejections(t *testing.T) {
	for name, change := range map[string]func(*forwardFixture, *ForwardRequest){
		"rpc-chain":         func(f *forwardFixture, _ *ForwardRequest) { f.chain = big.NewInt(10) },
		"domain-chain":      func(f *forwardFixture, _ *ForwardRequest) { f.domainChain = big.NewInt(10) },
		"verifier":          func(f *forwardFixture, _ *ForwardRequest) { f.verifier = common.Address{} },
		"salt-field":        func(f *forwardFixture, _ *ForwardRequest) { f.fields = [1]byte{31} },
		"extensions":        func(f *forwardFixture, _ *ForwardRequest) { f.extensions = []*big.Int{big.NewInt(1)} },
		"expired":           func(_ *forwardFixture, r *ForwardRequest) { r.Deadline = 1000 },
		"deadline-overflow": func(_ *forwardFixture, r *ForwardRequest) { r.Deadline = 1 << 48 },
		"negative-value":    func(_ *forwardFixture, r *ForwardRequest) { r.Value = big.NewInt(-1) },
		"malformed-return":  func(f *forwardFixture, _ *ForwardRequest) { f.malformed = true },
	} {
		t.Run(name, func(t *testing.T) {
			e, w, f, req := forwardSetup(t)
			change(f, &req)
			if _, err := SignForwardRequest(context.Background(), f, e.Keys, w, big.NewInt(1), req); err == nil {
				t.Fatal("invalid binding accepted")
			}
		})
	}
}

func TestPinnedV6AddressesAndABI(t *testing.T) {
	ids := []int64{1, 10, 8453, 42161, 84532, 421614, 11155111, 11155420}
	pairs := make([][]any, 0, len(ids))
	for _, id := range ids {
		address, err := ForwarderAddress(big.NewInt(id))
		if err != nil {
			t.Fatal(err)
		}
		pairs = append(pairs, []any{id, strings.ToLower(address.Hex())})
	}
	encoded, err := json.Marshal(pairs)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(encoded)
	if hex.EncodeToString(sum[:]) != "2529ebbf4c7a7855d1d91e210fa75630ba451aa0185790d54cd17e2778703932" {
		t.Fatal("V6 address projection changed")
	}
	for _, id := range []*big.Int{nil, big.NewInt(0), big.NewInt(-1), big.NewInt(137), new(big.Int).Lsh(big.NewInt(1), 65)} {
		if _, err := ForwarderAddress(id); err == nil {
			t.Fatal("unsupported chain accepted")
		}
	}
	// Preserve original field order in raw messages for the upstream JS checksum.
	raw, err := os.ReadFile("forwarder.abi.json")
	if err != nil {
		t.Fatal(err)
	}
	var entries []json.RawMessage
	if err = json.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, len(entries))
	for i, entry := range entries {
		var compact bytes.Buffer
		if err = json.Compact(&compact, entry); err != nil {
			t.Fatal(err)
		}
		lines[i] = compact.String()
	}
	sort.Strings(lines)
	sum = sha256.Sum256([]byte(strings.Join(lines, "\n")))
	if hex.EncodeToString(sum[:]) != "0bb81fff2d9988faf96902ab3910e3af3ca306a40c1baebb8d19a1f262a59f0b" {
		t.Fatal("retained ABI integrity changed")
	}
}
