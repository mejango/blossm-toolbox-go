package dia

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
)

type Claims struct {
	Issuer    string         `json:"iss"`
	Audience  []string       `json:"aud"`
	Subject   string         `json:"sub"`
	Expires   int64          `json:"exp"`
	NotBefore int64          `json:"nbf"`
	ID        string         `json:"jti"`
	Context   SessionContext `json:"context"`
	Kind      string         `json:"kind"`
}

type JWT struct {
	Signer    ES256Signer
	VerifyKey *ecdsa.PublicKey
	Issuer    string
	Audience  string
	Now       func() time.Time
}

var errToken = errors.New("invalid token")
var rawURL = base64.RawURLEncoding.Strict()

func (j JWT) validate(c Claims, kind string) error {
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

func (j JWT) Sign(ctx context.Context, c Claims) (string, error) {
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

func (j JWT) Verify(token, kind string) (Claims, error) {
	fail := func() (Claims, error) { return Claims{}, errToken }
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
	if strictJSON(header, &h) != nil || h.Alg != "ES256" || h.Typ != "JWT" {
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
	var c Claims
	if strictJSON(body, &c) != nil || j.validate(c, kind) != nil {
		return fail()
	}
	return c, nil
}

// strictJSON rejects duplicate keys, unknown fields and trailing JSON values.
func strictJSON(b []byte, dst any) error {
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
func (j JWT) PublicKey() ([]byte, error) {
	if !validPublicKey(j.VerifyKey) {
		return nil, errToken
	}
	b, e := x509.MarshalPKIXPublicKey(j.VerifyKey)
	if e != nil {
		return nil, e
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), nil
}

const passwordIterations = 600000

// HashPassword encodes PBKDF2-HMAC-SHA256 parameters, a random salt and a 32-byte key.
func HashPassword(password string) (string, error) {
	if len(password) == 0 || len(password) > 4096 {
		return "", errors.New("invalid password length")
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return "", e
	}
	key, e := pbkdf2.Key(sha256.New, password, salt, passwordIterations, 32)
	if e != nil {
		return "", e
	}
	return "pbkdf2-sha256$" + strconv.Itoa(passwordIterations) + "$" + rawURL.EncodeToString(salt) + "$" + rawURL.EncodeToString(key), nil
}

// CheckPassword takes the plaintext password followed by its stored hash.
func CheckPassword(password, encoded string) bool {
	if len(password) == 0 || len(password) > 4096 || len(encoded) > 256 {
		return false
	}
	p := strings.Split(encoded, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	n, e := strconv.Atoi(p[1])
	if e != nil || n < passwordIterations || n > 1000000 || strconv.Itoa(n) != p[1] {
		return false
	}
	salt, e := rawURL.DecodeString(p[2])
	if e != nil || len(salt) != 16 || rawURL.EncodeToString(salt) != p[2] {
		return false
	}
	want, e := rawURL.DecodeString(p[3])
	if e != nil || len(want) != 32 || rawURL.EncodeToString(want) != p[3] {
		return false
	}
	got, e := pbkdf2.Key(sha256.New, password, salt, n, 32)
	return e == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Seal prefixes a random GCM nonce to the authenticated ciphertext.
func Seal(key, plaintext []byte) ([]byte, error) {
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, g.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	return g.Seal(nonce, nonce, plaintext, nil), nil
}

func Open(key, ciphertext []byte) ([]byte, error) {
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	if len(ciphertext) < g.NonceSize()+g.Overhead() {
		return nil, errors.New("invalid ciphertext")
	}
	return g.Open(nil, ciphertext[:g.NonceSize()], ciphertext[g.NonceSize():], nil)
}
