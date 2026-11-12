// Package evm prepares locally signed EVM transactions with durable, replayable submission.
package evm

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// Cipher must provide authenticated encryption, binding ciphertext to aad.
// Implementations must not log plaintext. Ownership authorization belongs to the consumer.
type Cipher interface {
	Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error)
	Open(ctx context.Context, ciphertext, aad []byte) ([]byte, error)
}

type Wallet struct {
	ID           string         `json:"id"`
	Owner        string         `json:"owner"`
	Address      common.Address `json:"address"`
	EncryptedKey []byte         `json:"encryptedKey"`
}

func (w Wallet) aad() []byte {
	b, _ := json.Marshal([]string{"blossm.evm.custody.v1", w.ID, w.Owner, w.Address.Hex()})
	return b
}

func CreateWallet(ctx context.Context, cipher Cipher, id, owner string) (Wallet, error) {
	if cipher == nil || id == "" || owner == "" {
		return Wallet{}, errors.New("cipher, wallet ID and owner required")
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		return Wallet{}, errors.New("key generation failed")
	}
	defer clearPrivateKey(key)
	w := Wallet{ID: id, Owner: owner, Address: crypto.PubkeyToAddress(key.PublicKey)}
	raw := crypto.FromECDSA(key)
	defer clear(raw)
	w.EncryptedKey, err = cipher.Seal(ctx, raw, w.aad())
	if err != nil || len(w.EncryptedKey) == 0 {
		return Wallet{}, errors.New("key encryption failed")
	}
	return w, nil
}

func openKey(ctx context.Context, cipher Cipher, w Wallet) (*ecdsa.PrivateKey, error) {
	if cipher == nil || w.ID == "" || w.Owner == "" || len(w.EncryptedKey) == 0 {
		return nil, errors.New("invalid wallet custody")
	}
	raw, err := cipher.Open(ctx, w.EncryptedKey, w.aad())
	defer clear(raw)
	if err != nil {
		return nil, errors.New("key decryption failed")
	}
	key, err := crypto.ToECDSA(raw)
	if err != nil {
		return nil, errors.New("invalid custody key")
	}
	if crypto.PubkeyToAddress(key.PublicKey) != w.Address {
		clearPrivateKey(key)
		return nil, errors.New("custody address mismatch")
	}
	return key, nil
}

// clearPrivateKey is best-effort: Go and signing libraries may retain other copies.
func clearPrivateKey(key *ecdsa.PrivateKey) {
	clear(key.D.Bits())
	key.D.SetInt64(0)
}
