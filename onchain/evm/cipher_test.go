package evm

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"math/big"
	"testing"
)

func newCipher(t *testing.T) *AESGCMCipher {
	t.Helper()
	c, err := NewAESGCMCipher(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAESGCMFormatAndRandomNonce(t *testing.T) {
	c := newCipher(t)
	ctx := context.Background()
	secret := []byte("private material")
	aad := []byte("owner binding")
	a, err := c.Seal(ctx, secret, aad)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Seal(ctx, secret, aad)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonce reused")
	}
	raw, err := c.Open(ctx, a, aad)
	if err != nil || !bytes.Equal(raw, secret) {
		t.Fatal("roundtrip", err)
	}
	if _, err = c.Open(ctx, a, []byte("other owner")); err == nil {
		t.Fatal("AAD not bound")
	}
	for _, bad := range [][]byte{nil, {}, a[:12], append([]byte{2}, a[1:]...)} {
		if _, err = c.Open(ctx, bad, aad); err == nil {
			t.Fatal("malformed ciphertext accepted")
		}
	}
	if _, err = NewAESGCMCipher(make([]byte, 16)); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestPrivateScalarBackingWordsCleared(t *testing.T) {
	key := &ecdsa.PrivateKey{D: new(big.Int).SetBytes(bytes.Repeat([]byte{0xff}, 32))}
	words := key.D.Bits() // Retain the backing slice: zero length alone is insufficient.
	clearPrivateKey(key)
	if key.D.Sign() != 0 {
		t.Fatal("scalar not reset")
	}
	for _, word := range words {
		if word != 0 {
			t.Fatal("private scalar backing memory retained")
		}
	}
}
