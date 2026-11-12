package evm

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

// AESGCMCipher provides AES-256 authenticated encryption with fresh random
// 96-bit nonces. Store the 32-byte encryption key outside wallet storage. A
// ciphertext is version byte 1 || nonce || GCM ciphertext and authentication tag.
// Key rotation/version routing belongs to the consumer's key-management policy.
// Do not exceed 2^32 encryptions with a single key (GCM random-nonce bound).
type AESGCMCipher struct{ aead cipher.AEAD }

func NewAESGCMCipher(key []byte) (*AESGCMCipher, error) {
	if len(key) != 32 {
		return nil, errors.New("AES-256 custody key must contain 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("invalid custody encryption key")
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, errors.New("custody cipher initialization failed")
	}
	return &AESGCMCipher{aead: aead}, nil
}

func (c *AESGCMCipher) Seal(ctx context.Context, plaintext, aad []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.aead == nil {
		return nil, errors.New("custody cipher not initialized")
	}
	prefix := make([]byte, 1+c.aead.NonceSize())
	prefix[0] = 1
	if _, err := rand.Read(prefix[1:]); err != nil {
		return nil, errors.New("custody nonce generation failed")
	}
	return c.aead.Seal(prefix, prefix[1:], plaintext, aad), nil
}

func (c *AESGCMCipher) Open(ctx context.Context, ciphertext, aad []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.aead == nil {
		return nil, errors.New("custody cipher not initialized")
	}
	offset := 1 + c.aead.NonceSize()
	if len(ciphertext) < offset+c.aead.Overhead() || ciphertext[0] != 1 {
		return nil, errors.New("invalid custody ciphertext")
	}
	raw, err := c.aead.Open(nil, ciphertext[1:offset], ciphertext[offset:], aad)
	if err != nil {
		return nil, errors.New("custody authentication failed")
	}
	return raw, nil
}
