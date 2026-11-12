// Package wallet binds custody and transaction submission to authenticated Dia owners.
package wallet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/evm"
	"github.com/ethereum/go-ethereum/common"
)

type Authenticator interface {
	AuthenticateAccess(context.Context, string) (dia.Claims, error)
}
type Service struct {
	Auth   Authenticator
	Store  *eventstore.Store
	Keys   evm.Cipher
	Chains map[string]evm.Engine
}
type Address struct {
	ID      string         `json:"id"`
	Address common.Address `json:"address"`
}

func id(parts ...string) string {
	b, _ := json.Marshal(parts)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func root(network, domain, key string) eventstore.Root {
	return eventstore.Root{Network: network, Service: "wallet", Domain: domain, ID: key}
}

func validRequestKey(key string) bool { return strings.TrimSpace(key) != "" && len(key) <= 256 }
func event(r eventstore.Root, action, key string, version int64, payload any) eventstore.Input {
	b, _ := json.Marshal(payload)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return eventstore.Input{Root: r, Action: action, Key: key, Expected: &version, Created: now, Payload: b, Transaction: eventstore.Transaction{ID: key, Issued: now}}
}
func (s Service) owner(ctx context.Context, token string) (dia.Claims, error) {
	if s.Auth == nil || s.Store == nil || s.Keys == nil {
		return dia.Claims{}, dia.ErrUnavailable
	}
	c, e := s.Auth.AuthenticateAccess(ctx, token)
	if e != nil {
		return c, e
	}
	principal, account := c.Context.Principal, c.Context.Account
	if principal.ID == "" || account.ID == "" || c.Context.Network == "" ||
		principal.Network != c.Context.Network || account.Network != c.Context.Network ||
		principal.Service != "core" || principal.Domain != "principal" ||
		account.Service != "core" || account.Domain != "account" {
		return c, dia.ErrForbidden
	}
	pa, e := s.Store.Aggregate(ctx, principal)
	if e != nil {
		return c, e
	}
	aa, e := s.Store.Aggregate(ctx, account)
	if e != nil {
		return c, e
	}
	var p dia.PrincipalState
	var a dia.AccountState
	if pa.Version < 0 || aa.Version < 0 {
		return c, dia.ErrForbidden
	}
	if e = json.Unmarshal(pa.State, &p); e != nil {
		return c, e
	}
	if e = json.Unmarshal(aa.State, &a); e != nil {
		return c, e
	}
	if p.Subject != account || a.Principal != principal {
		return c, dia.ErrForbidden
	}
	return c, nil
}
func (s Service) Create(ctx context.Context, token, key string) (Address, error) {
	c, e := s.owner(ctx, token)
	if e != nil {
		return Address{}, e
	}
	if !validRequestKey(key) {
		return Address{}, dia.ErrInvalid
	}
	r := root(c.Context.Network, "address", id(c.Context.Principal.Key(), key))
	a, e := s.Store.Aggregate(ctx, r)
	if e != nil {
		return Address{}, e
	}
	var w evm.Wallet
	if a.Version < 0 {
		w, e = evm.CreateWallet(ctx, s.Keys, r.ID, c.Context.Principal.Key())
		if e != nil {
			return Address{}, e
		}
		_, e = s.Store.Append(ctx, []eventstore.Input{event(r, "create", "wallet/create/"+r.ID, 0, w)})
		if e != nil {
			if !errors.Is(e, eventstore.ErrConflict) && !errors.Is(e, eventstore.ErrIdempotency) {
				return Address{}, e
			}
			a, e = s.Store.Aggregate(ctx, r)
			if e != nil {
				return Address{}, e
			}
			if e = json.Unmarshal(a.State, &w); e != nil {
				return Address{}, e
			}
		}
	} else if e = json.Unmarshal(a.State, &w); e != nil {
		return Address{}, e
	}
	if w.Owner != c.Context.Principal.Key() {
		return Address{}, dia.ErrForbidden
	}
	return Address{w.ID, w.Address}, nil
}
func (s Service) load(ctx context.Context, token, walletID string) (dia.Claims, evm.Wallet, error) {
	c, e := s.owner(ctx, token)
	if e != nil {
		return c, evm.Wallet{}, e
	}
	a, e := s.Store.Aggregate(ctx, root(c.Context.Network, "address", walletID))
	if e != nil {
		return c, evm.Wallet{}, e
	}
	var w evm.Wallet
	if e = json.Unmarshal(a.State, &w); e != nil {
		return c, w, e
	}
	if a.Version < 0 || w.Owner != c.Context.Principal.Key() {
		return c, w, dia.ErrForbidden
	}
	return c, w, nil
}
func (s Service) engine(network, chain string) (evm.Engine, error) {
	engine, ok := s.Chains[chain]
	if !ok || engine.ChainID == nil || engine.ChainID.String() != chain {
		return engine, dia.ErrInvalid
	}
	engine.Keys = s.Keys
	engine.Journal = Journal{Store: s.Store, Network: network}
	return engine, nil
}
func (s Service) Prepare(ctx context.Context, token, walletID, chain string, req evm.Request) (evm.Prepared, error) {
	// Validate the caller's original key before namespacing: a hash is always nonempty.
	if !validRequestKey(req.ID) {
		return evm.Prepared{}, dia.ErrInvalid
	}
	c, w, e := s.load(ctx, token, walletID)
	if e != nil {
		return evm.Prepared{}, e
	}
	engine, e := s.engine(c.Context.Network, chain)
	if e != nil {
		return evm.Prepared{}, e
	}
	req.ID = id(w.Owner, w.ID, req.ID)
	return engine.Prepare(ctx, w, req)
}
func (s Service) Submit(ctx context.Context, token, walletID, chain, transactionID string) (evm.Observation, error) {
	c, w, e := s.load(ctx, token, walletID)
	if e != nil {
		return evm.Observation{}, e
	}
	engine, e := s.engine(c.Context.Network, chain)
	if e != nil {
		return evm.Observation{}, e
	}
	p, e := engine.Journal.Get(ctx, transactionID)
	if e != nil {
		return evm.Observation{}, e
	}
	if p.From != w.Address {
		return evm.Observation{}, dia.ErrForbidden
	}
	if p.ChainID != chain {
		return evm.Observation{}, dia.ErrInvalid
	}
	o, submitErr := engine.Submit(ctx, transactionID)
	recorded, observationErr := s.observe(ctx, c.Context.Network, o, true)
	return recorded, errors.Join(submitErr, observationErr)
}
func (s Service) Reconcile(ctx context.Context, token, walletID, chain, transactionID string, policy evm.PollPolicy) (evm.Observation, error) {
	c, w, e := s.load(ctx, token, walletID)
	if e != nil {
		return evm.Observation{}, e
	}
	engine, e := s.engine(c.Context.Network, chain)
	if e != nil {
		return evm.Observation{}, e
	}
	p, e := engine.Journal.Get(ctx, transactionID)
	if e != nil {
		return evm.Observation{}, e
	}
	if p.From != w.Address {
		return evm.Observation{}, dia.ErrForbidden
	}
	if p.ChainID != chain {
		return evm.Observation{}, dia.ErrInvalid
	}
	o, reconcileErr := engine.Reconcile(ctx, transactionID, policy)
	recorded, observationErr := s.observe(ctx, c.Context.Network, o, false)
	return recorded, errors.Join(reconcileErr, observationErr)
}
func (s Service) observe(ctx context.Context, network string, o evm.Observation, submission bool) (evm.Observation, error) {
	r := root(network, "observation", o.ID)
	a, e := s.Store.Aggregate(ctx, r)
	if e != nil {
		return o, e
	}
	if a.Version >= 0 {
		var previous evm.Observation
		if e = json.Unmarshal(a.State, &previous); e != nil {
			return o, e
		}
		// A send acknowledgement has no authority to supersede receipt evidence.
		if previous == o || (submission && (previous.State == evm.Succeeded || previous.State == evm.Failed)) {
			return previous, nil
		}
	}
	digest, e := eventstore.Digest(o)
	if e != nil {
		return o, e
	}
	_, e = s.Store.Append(ctx, []eventstore.Input{event(r, "observe", fmt.Sprintf("wallet/observe/%s/%d/%s", o.ID, a.Version+1, digest), a.Version+1, o)})
	return o, e
}
