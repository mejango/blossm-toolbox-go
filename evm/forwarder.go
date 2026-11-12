package evm

import (
	"context"
	_ "embed"
	"errors"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	ethmath "github.com/ethereum/go-ethereum/common/math"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/signer/core/apitypes"
)

//go:embed forwarder.abi.json
var forwarderJSON string

// ForwarderABI returns a fresh ABI so callers cannot mutate the package's owner.
func ForwarderABI() (abi.ABI, error) { return abi.JSON(strings.NewReader(forwarderJSON)) }

// ForwarderAddress is pinned to Bananapus/deploy-all-v6 c62b3ac81bfc5603b6771e8ad801eb2236022947.
func ForwarderAddress(chain *big.Int) (common.Address, error) {
	if chain == nil || !chain.IsUint64() {
		return common.Address{}, errors.New("unsupported V6 chain")
	}
	switch chain.Uint64() {
	case 1, 10, 8453, 42161, 11155111, 84532, 11155420, 421614:
		return common.HexToAddress("0x3ba60b60933916a7c87d0860dcee62a0ce34e3e2"), nil
	default:
		return common.Address{}, errors.New("unsupported V6 chain")
	}
}

// ForwardClient is implemented by ethclient.Client. Block-hash calls bind domain
// and nonce to the same state even if the node's latest block changes mid-request.
type ForwardClient interface {
	ChainID(context.Context) (*big.Int, error)
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	CallContractAtHash(context.Context, ethereum.CallMsg, common.Hash) ([]byte, error)
}

type ForwardRequest struct {
	To       common.Address
	Value    *big.Int
	Gas      *big.Int
	Deadline uint64 // uint48 Unix seconds, checked against the selected block
	Data     []byte
}

type ForwardCall struct {
	To        common.Address
	Data      []byte
	Value     *big.Int
	Nonce     *big.Int
	Digest    common.Hash
	Signature []byte
	BlockHash common.Hash
}

// SignForwardRequest prepares execute calldata only. It does not relay or submit.
// Submit it via Engine.Prepare/Submit with a funded authorized gas-paying wallet.
// Targets must trust this forwarder; execution enforces target trust and deadline.
func SignForwardRequest(ctx context.Context, rpc ForwardClient, cipher Cipher, w Wallet, chain *big.Int, req ForwardRequest) (ForwardCall, error) {
	result := ForwardCall{}
	address, err := ForwarderAddress(chain)
	if err != nil {
		return result, err
	}
	if rpc == nil || req.To == (common.Address{}) || !uint256(req.Value) || !uint256(req.Gas) || req.Gas.Sign() == 0 || req.Deadline >= (1<<48) {
		return result, errors.New("invalid forward request")
	}
	actual, err := rpc.ChainID(ctx)
	if err != nil {
		return result, err
	}
	if actual == nil || actual.Cmp(chain) != 0 {
		return result, errors.New("RPC chain mismatch")
	}
	block, err := rpc.HeaderByNumber(ctx, nil)
	if err != nil {
		return result, err
	}
	if block == nil || block.Number == nil || req.Deadline <= block.Time {
		return result, errors.New("forward request expired or missing block")
	}
	contract, err := ForwarderABI()
	if err != nil {
		return result, err
	}
	call := func(name string, args ...any) ([]any, error) {
		data, err := contract.Pack(name, args...)
		if err != nil {
			return nil, err
		}
		output, err := rpc.CallContractAtHash(ctx, ethereum.CallMsg{To: &address, Data: data}, block.Hash())
		if err != nil {
			return nil, err
		}
		return contract.Unpack(name, output)
	}
	domain, err := call("eip712Domain")
	if err != nil {
		return result, err
	}
	if len(domain) != 7 {
		return result, errors.New("invalid EIP712 domain")
	}
	fields, ok := domain[0].([1]byte)
	if !ok || fields[0] != 15 {
		return result, errors.New("unsupported EIP712 domain fields")
	}
	name, nameOK := domain[1].(string)
	version, versionOK := domain[2].(string)
	domainChain, chainOK := domain[3].(*big.Int)
	verifier, verifierOK := domain[4].(common.Address)
	extensions, extensionsOK := domain[6].([]*big.Int)
	if !nameOK || !versionOK || name == "" || version == "" || !chainOK || domainChain == nil || domainChain.Cmp(chain) != 0 || !verifierOK || verifier != address || !extensionsOK || len(extensions) != 0 {
		return result, errors.New("forwarder domain binding mismatch")
	}
	nonceResult, err := call("nonces", w.Address)
	if err != nil {
		return result, err
	}
	if len(nonceResult) != 1 {
		return result, errors.New("invalid forwarder nonce")
	}
	nonce, ok := nonceResult[0].(*big.Int)
	if !ok || !uint256(nonce) {
		return result, errors.New("invalid forwarder nonce")
	}
	typed := apitypes.TypedData{
		Types: apitypes.Types{
			"EIP712Domain":   {{Name: "name", Type: "string"}, {Name: "version", Type: "string"}, {Name: "chainId", Type: "uint256"}, {Name: "verifyingContract", Type: "address"}},
			"ForwardRequest": {{Name: "from", Type: "address"}, {Name: "to", Type: "address"}, {Name: "value", Type: "uint256"}, {Name: "gas", Type: "uint256"}, {Name: "nonce", Type: "uint256"}, {Name: "deadline", Type: "uint48"}, {Name: "data", Type: "bytes"}},
		},
		PrimaryType: "ForwardRequest",
		Domain:      apitypes.TypedDataDomain{Name: name, Version: version, ChainId: (*ethmath.HexOrDecimal256)(domainChain), VerifyingContract: address.Hex()},
		Message:     apitypes.TypedDataMessage{"from": w.Address.Hex(), "to": req.To.Hex(), "value": req.Value.String(), "gas": req.Gas.String(), "nonce": nonce.String(), "deadline": new(big.Int).SetUint64(req.Deadline).String(), "data": hexutil.Encode(req.Data)},
	}
	digest, _, err := apitypes.TypedDataAndHash(typed)
	if err != nil {
		return result, err
	}
	key, err := openKey(ctx, cipher, w)
	if err != nil {
		return result, err
	}
	defer clearPrivateKey(key)
	signature, err := crypto.Sign(digest, key)
	if err != nil {
		return result, errors.New("typed signing failed")
	}
	signature[64] += 27
	tuple := struct {
		From      common.Address
		To        common.Address
		Value     *big.Int
		Gas       *big.Int
		Deadline  *big.Int
		Data      []byte
		Signature []byte
	}{w.Address, req.To, req.Value, req.Gas, new(big.Int).SetUint64(req.Deadline), req.Data, signature}
	data, err := contract.Pack("execute", tuple)
	if err != nil {
		return result, err
	}
	return ForwardCall{To: address, Data: data, Value: new(big.Int).Set(req.Value), Nonce: new(big.Int).Set(nonce), Digest: common.BytesToHash(digest), Signature: signature, BlockHash: block.Hash()}, nil
}
