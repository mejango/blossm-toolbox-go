// Package audit encrypts and authenticates private block exports. Stored internal
// blocks remain separate from explicitly exported, signed transport envelopes.
package audit

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"math/big"

	"blossm.local/backend/eventstore"
)

const protocol = "blossm.private-block.v1"

type Envelope struct {
	Protocol   string `json:"protocol"`
	Producer   string `json:"producer"`
	Algorithm  string `json:"algorithm"`
	Hash       string `json:"hash"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
	Signature  []byte `json:"signature"`
}

func aead(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("AES256 export key required")
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func additional(e Envelope) ([]byte, error) {
	return eventstore.Encode([]string{e.Protocol, e.Producer, e.Algorithm, e.Hash})
}

// Seal requires an ES256 P1363 signer over the supplied message bytes. It signs
// ciphertext and metadata together; the producer identity is never inferred.
func Seal(ctx context.Context, b eventstore.Block, producer string, key []byte, signer eventstore.Signer) (Envelope, error) {
	out := Envelope{Protocol: protocol, Producer: producer, Algorithm: "ES256-P1363", Hash: b.Hash}
	if producer == "" || signer == nil {
		return out, errors.New("producer and signer required")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if b.Hash == "" {
		return out, errors.New("block hash required")
	}
	unsigned := b
	unsigned.Hash = ""
	unsigned.Signature = nil
	hash, e := eventstore.Digest(unsigned)
	if e != nil || hash != b.Hash {
		return out, errors.New("invalid block hash")
	}
	c, e := aead(key)
	if e != nil {
		return out, e
	}
	raw, e := eventstore.Encode(b)
	if e != nil {
		return out, e
	}
	out.Nonce = make([]byte, c.NonceSize())
	if _, e = rand.Read(out.Nonce); e != nil {
		return out, e
	}
	aad, e := additional(out)
	if e != nil {
		return out, e
	}
	out.Ciphertext = c.Seal(nil, out.Nonce, raw, aad)
	message, e := eventstore.Encode(out)
	if e != nil {
		return out, e
	}
	out.Signature, e = signer.Sign(ctx, message)
	if e != nil {
		return out, e
	}
	if len(out.Signature) != 64 {
		return out, errors.New("ES256 signature must be64bytes")
	}
	return out, nil
}
func Open(ctx context.Context, in Envelope, expectedProducer string, key []byte, verifier eventstore.Verifier) (eventstore.Block, error) {
	var b eventstore.Block
	if verifier == nil || in.Protocol != protocol || in.Algorithm != "ES256-P1363" || expectedProducer == "" || in.Producer != expectedProducer || len(in.Signature) != 64 {
		return b, errors.New("invalid export authority or protocol")
	}
	signature := in.Signature
	in.Signature = nil
	message, e := eventstore.Encode(in)
	if e != nil {
		return b, e
	}
	if e = verifier.Verify(ctx, message, signature); e != nil {
		return b, e
	}
	c, e := aead(key)
	if e != nil {
		return b, e
	}
	if len(in.Nonce) != c.NonceSize() {
		return b, errors.New("invalid export nonce")
	}
	aad, e := additional(in)
	if e != nil {
		return b, e
	}
	raw, e := c.Open(nil, in.Nonce, in.Ciphertext, aad)
	if e != nil {
		return b, errors.New("block decryption failed")
	}
	if e = eventstore.Decode(raw, &b); e != nil {
		return b, e
	}
	unsigned := b
	unsigned.Hash = ""
	unsigned.Signature = nil
	hash, e := eventstore.Digest(unsigned)
	if e != nil || hash != b.Hash || hash != in.Hash {
		return b, errors.New("export block hash mismatch")
	}
	return b, nil
}

// ES256 uses standard-library P-256 and JOSE's fixed-width r||s signature encoding.
// Cloud KMS adapters expose matching SignES256/VerifyES256 methods for composition.
type ES256 struct {
	Private *ecdsa.PrivateKey
	Public  *ecdsa.PublicKey
}

func (s ES256) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if s.Private == nil || s.Private.Curve != elliptic.P256() {
		return nil, errors.New("P256 signing key required")
	}
	digest := sha256.Sum256(message)
	r, v, e := ecdsa.Sign(rand.Reader, s.Private, digest[:])
	if e != nil {
		return nil, e
	}
	out := make([]byte, 64)
	r.FillBytes(out[:32])
	v.FillBytes(out[32:])
	return out, nil
}
func (s ES256) Verify(ctx context.Context, message, signature []byte) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	key := s.Public
	if key == nil && s.Private != nil {
		key = &s.Private.PublicKey
	}
	if key == nil || key.Curve != elliptic.P256() || len(signature) != 64 {
		return errors.New("invalid ES256 verifier")
	}
	digest := sha256.Sum256(message)
	if !ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return errors.New("invalid ES256 signature")
	}
	return nil
}
