package token

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"strings"
	"time"
)

type Claims[C any] struct {
	Issuer    string   `json:"iss"`
	Audience  []string `json:"aud"`
	Subject   string   `json:"sub"`
	Expires   int64    `json:"exp"`
	NotBefore int64    `json:"nbf"`
	ID        string   `json:"jti"`
	Context   C        `json:"context"`
	Kind      string   `json:"kind"`
}

type JWT[C any] struct {
	Signer    ES256Signer
	VerifyKey *ecdsa.PublicKey
	Issuer    string
	Audience  string
	Now       func() time.Time
}

var errToken = errors.New("invalid token")
var rawURL = base64.RawURLEncoding.Strict()

func (j JWT[C]) validate(c Claims[C], kind string) error {
	now := time.Now()
	if j.Now != nil {
		now = j.Now()
	}
	if j.Issuer == "" || j.Audience == "" || c.Issuer != j.Issuer || kind == "" || c.Kind != kind || c.Subject == "" || c.ID == "" || c.Expires <= now.Unix() || c.NotBefore > now.Unix() || c.NotBefore < 0 || c.Expires <= c.NotBefore {
		return errToken
	}
	for _, a := range c.Audience {
		if a == j.Audience {
			return nil
		}
	}
	return errToken
}

func (j JWT[C]) Sign(ctx context.Context, c Claims[C]) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !validPublicKey(j.VerifyKey) || j.Signer == nil {
		return "", errToken
	}
	if err := j.validate(c, c.Kind); err != nil {
		return "", err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	input := rawURL.EncodeToString([]byte(`{"alg":"ES256","typ":"JWT"}`)) + "." + rawURL.EncodeToString(b)
	if len(input)+1+rawURL.EncodedLen(64) > 16384 {
		return "", errToken
	}
	signature, err := j.Signer.SignES256(ctx, []byte(input))
	if err != nil {
		return "", err
	}
	if err = ctx.Err(); err != nil {
		return "", err
	}
	if err = j.validate(c, c.Kind); err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(input))
	if len(signature) != 64 || !ecdsa.Verify(j.VerifyKey, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		return "", errToken
	}
	return input + "." + rawURL.EncodeToString(signature), nil
}

func (j JWT[C]) Verify(token, kind string) (Claims[C], error) {
	fail := func() (Claims[C], error) { return Claims[C]{}, errToken }
	if !validPublicKey(j.VerifyKey) || len(token) > 16384 {
		return fail()
	}
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return fail()
	}
	for _, part := range p {
		decoded, e := rawURL.DecodeString(part)
		if e != nil || rawURL.EncodeToString(decoded) != part {
			return fail()
		}
	}
	header, e := rawURL.DecodeString(p[0])
	if e != nil {
		return fail()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(header, &fields) != nil || len(fields) != 2 || fields["alg"] == nil || fields["typ"] == nil {
		return fail()
	}
	var h struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if StrictJSON(header, &h) != nil || h.Alg != "ES256" || h.Typ != "JWT" {
		return fail()
	}
	sig, e := rawURL.DecodeString(p[2])
	if e != nil || len(sig) != 64 {
		return fail()
	}
	digest := sha256.Sum256([]byte(p[0] + "." + p[1]))
	if !ecdsa.Verify(j.VerifyKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		return fail()
	}
	body, e := rawURL.DecodeString(p[1])
	if e != nil {
		return fail()
	}
	var claimFields map[string]json.RawMessage
	if json.Unmarshal(body, &claimFields) != nil || len(claimFields) != 8 {
		return fail()
	}
	for _, name := range []string{"iss", "aud", "sub", "exp", "nbf", "jti", "context", "kind"} {
		v, ok := claimFields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return fail()
		}
	}
	var c Claims[C]
	if StrictJSON(body, &c) != nil || j.validate(c, kind) != nil {
		return fail()
	}
	return c, nil
}

// StrictJSON rejects duplicate keys, unknown fields and trailing JSON values.
func StrictJSON(b []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		t, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				key, ok := k.(string)
				if !ok || seen[key] {
					return errToken
				}
				seen[key] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
		default:
			return errToken
		}
		_, e = d.Token()
		return e
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errToken
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(dst)
}

// PublicKey returns a PEM-encoded PKIX public key.
func (j JWT[C]) PublicKey() ([]byte, error) {
	if !validPublicKey(j.VerifyKey) {
		return nil, errToken
	}
	b, e := x509.MarshalPKIXPublicKey(j.VerifyKey)
	if e != nil {
		return nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), nil
}
