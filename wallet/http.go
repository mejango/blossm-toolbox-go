package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/evm"
)

type transactionRequest struct {
	Wallet string `json:"wallet"`
	Chain  string `json:"chain"`
	To     string `json:"to"`
	Value  string `json:"value"`
	Data   string `json:"data"`
	Gas    uint64 `json:"gas,string"`
	FeeCap string `json:"feeCap"`
	TipCap string `json:"tipCap"`
}

func uintValue(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok || v.Sign() < 0 || v.BitLen() > 256 || v.String() != s {
		return nil, dia.ErrInvalid
	}
	return v, nil
}

// Handler exposes only owner-authorized custody operations, never decrypted keys.
func (s Service) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		key := r.Header.Get("Idempotency-Key")
		read := func(v any) error {
			raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if e != nil {
				return dia.ErrInvalid
			}
			if e = eventstore.Decode(raw, v); e != nil {
				return dia.ErrInvalid
			}
			return nil
		}
		var result any
		var err error
		switch r.URL.Path {
		case "/commands/wallet/create":
			var in struct{}
			err = read(&in)
			if err == nil {
				result, err = s.Create(r.Context(), token, key)
			}
		case "/commands/wallet/prepare":
			var in transactionRequest
			err = read(&in)
			if err == nil {
				var value, fee, tip *big.Int
				var data []byte
				value, err = uintValue(in.Value)
				if err == nil {
					fee, err = uintValue(in.FeeCap)
				}
				if err == nil {
					tip, err = uintValue(in.TipCap)
				}
				if err == nil {
					data, err = hexutil.Decode(in.Data)
					if err != nil {
						err = dia.ErrInvalid
					}
				}
				if !common.IsHexAddress(in.To) || key == "" {
					err = dia.ErrInvalid
				}
				if err == nil {
					result, err = s.Prepare(r.Context(), token, in.Wallet, in.Chain, evm.Request{ID: key, To: common.HexToAddress(in.To), Value: value, Data: data, Gas: in.Gas, FeeCap: fee, TipCap: tip})
				}
			}
		case "/commands/wallet/submit", "/commands/wallet/reconcile":
			var in struct {
				Wallet        string `json:"wallet"`
				Chain         string `json:"chain"`
				Transaction   string `json:"transaction"`
				Confirmations uint64 `json:"confirmations,string"`
			}
			err = read(&in)
			if err == nil {
				if r.URL.Path == "/commands/wallet/submit" {
					result, err = s.Submit(r.Context(), token, in.Wallet, in.Chain, in.Transaction)
				} else {
					result, err = s.Reconcile(r.Context(), token, in.Wallet, in.Chain, in.Transaction, evm.PollPolicy{Attempts: 3, Interval: time.Second, Timeout: 15 * time.Second, Confirmations: in.Confirmations})
				}
			}
		default:
			w.WriteHeader(404)
			return
		}
		if err != nil {
			status := 500
			message := "operation failed"
			switch {
			case errors.Is(err, dia.ErrUnauthorized):
				status = 401
				message = "unauthorized"
			case errors.Is(err, dia.ErrForbidden):
				status = 403
				message = "forbidden"
			case errors.Is(err, dia.ErrInvalid):
				status = 400
				message = "invalid input"
			case errors.Is(err, evm.ErrConflict), errors.Is(err, evm.ErrNonceConflict), errors.Is(err, eventstore.ErrConflict):
				status = 409
				message = "conflict"
			}
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(struct {
				Error       string `json:"error"`
				Observation any    `json:"observation,omitempty"`
			}{message, result})
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
