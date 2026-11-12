package diaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"os"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/evm"
	"github.com/mejango/blossm-toolbox-go/runtimeenv"
	"github.com/mejango/blossm-toolbox-go/wallet"
)

func walletHandler(ctx context.Context, app *dia.Service, store *eventstore.Store) (http.Handler, func(), error) {
	raw := os.Getenv("EVM_RPC_URLS")
	if raw == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "wallet not configured", http.StatusServiceUnavailable)
		}), func() {}, nil
	}
	var urls map[string]string
	if e := json.Unmarshal([]byte(raw), &urls); e != nil || len(urls) == 0 {
		return nil, nil, errors.New("invalid EVM_RPC_URLS")
	}
	key, e := runtimeenv.Secret32("WALLET_ENCRYPTION_KEY")
	if e != nil {
		return nil, nil, e
	}
	cipher, e := evm.NewAESGCMCipher(key)
	clear(key)
	if e != nil {
		return nil, nil, e
	}
	clients := []*ethclient.Client{}
	closeAll := func() {
		for _, client := range clients {
			client.Close()
		}
	}
	engines := map[string]evm.Engine{}
	for chain, endpoint := range urls {
		id, ok := new(big.Int).SetString(chain, 10)
		if !ok || id.Sign() <= 0 || id.String() != chain {
			closeAll()
			return nil, nil, errors.New("invalid EVM chain")
		}
		parsed, e := url.Parse(endpoint)
		if e != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost"))) {
			closeAll()
			return nil, nil, errors.New("EVM RPC requires HTTPS or loopback HTTP")
		}
		client, e := ethclient.DialContext(ctx, endpoint)
		if e != nil {
			closeAll()
			return nil, nil, errors.New("EVM RPC construction failed")
		}
		clients = append(clients, client)
		engines[chain] = evm.Engine{RPC: client, ChainID: id}
	}
	return wallet.Service{Auth: app, Store: store, Keys: cipher, Chains: engines}.Handler(), closeAll, nil
}
