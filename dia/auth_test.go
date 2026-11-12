package dia

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

func testJWT(t *testing.T) (JWT, Claims) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return JWT{Signer: LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "issuer", Audience: "network", Now: func() time.Time { return time.Unix(100, 0) }}, Claims{Issuer: "issuer", Audience: []string{"network"}, Subject: "subject", Expires: 200, NotBefore: 100, ID: "id", Kind: "access"}
}

func rawToken(t *testing.T, j JWT, header, body string) string {
	t.Helper()
	input := rawURL.EncodeToString([]byte(header)) + "." + rawURL.EncodeToString([]byte(body))
	h := sha256.Sum256([]byte(input))
	r, s, e := ecdsa.Sign(rand.Reader, j.Signer.(LocalSigner).Key, h[:])
	if e != nil {
		t.Fatal(e)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + rawURL.EncodeToString(sig)
}

func TestJWT(t *testing.T) {
	j, c := testJWT(t)
	token, e := j.Sign(context.Background(), c)
	if e != nil {
		t.Fatal(e)
	}
	got, e := j.Verify(token, "access")
	if e != nil || got.ID != c.ID {
		t.Fatalf("verify: %+v %v", got, e)
	}
	parts := strings.Split(token, ".")
	sig, _ := rawURL.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatal("not JOSE raw signature")
	}
	pub, e := j.PublicKey()
	if e != nil {
		t.Fatal(e)
	}
	block, _ := pem.Decode(pub)
	if block == nil {
		t.Fatal("no PEM")
	}
	key, e := x509.ParsePKIXPublicKey(block.Bytes)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(key.(*ecdsa.PublicKey), h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("public key cannot verify JOSE signature")
	}

	verifier := j
	verifier.Signer = nil
	if _, e = verifier.Verify(token, "access"); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"refresh", ""} {
		if _, e = j.Verify(token, kind); e == nil {
			t.Fatal("accepted wrong kind")
		}
	}
	other, _ := testJWT(t)
	if _, e = other.Verify(token, "access"); e == nil {
		t.Fatal("accepted wrong key")
	}
	for _, bad := range []string{"", token + ".", parts[0] + "." + parts[1] + ".", strings.Repeat("x", 16385)} {
		if _, e = j.Verify(bad, "access"); e == nil {
			t.Fatal("accepted malformed token")
		}
	}
	sig[0] ^= 1
	if _, e = j.Verify(parts[0]+"."+parts[1]+"."+rawURL.EncodeToString(sig), "access"); e == nil {
		t.Fatal("accepted tampered signature")
	}
	body, _ := json.Marshal(c)
	for _, header := range []string{`{"alg":"none","typ":"JWT"}`, `{"alg":"ES384","typ":"JWT"}`, `{"alg":"ES256"}`, `{"alg":"ES256","typ":"JWT","crit":["x"]}`, `{"alg":"none","alg":"ES256","typ":"JWT"}`, `{"ALG":"ES256","typ":"JWT"}`} {
		if _, e = j.Verify(rawToken(t, j, header, string(body)), "access"); e == nil {
			t.Fatalf("accepted header %s", header)
		}
	}
	for name, mutate := range map[string]func(*Claims){"expired": func(c *Claims) { c.Expires = 100 }, "future": func(c *Claims) { c.NotBefore = 101 }, "issuer": func(c *Claims) { c.Issuer = "other" }, "audience": func(c *Claims) { c.Audience = []string{"other"} }, "subject": func(c *Claims) { c.Subject = "" }, "id": func(c *Claims) { c.ID = "" }, "kind": func(c *Claims) { c.Kind = "refresh" }} {
		t.Run(name, func(t *testing.T) {
			bad := c
			mutate(&bad)
			b, _ := json.Marshal(bad)
			if _, e = j.Verify(rawToken(t, j, `{"alg":"ES256","typ":"JWT"}`, string(b)), "access"); e == nil {
				t.Fatal("accepted invalid claim")
			}
		})
	}
	for _, bad := range []string{string(body) + `{}`, strings.Replace(string(body), `"nbf":100,`, "", 1), strings.Replace(string(body), `"nbf":100`, `"nbf":null`, 1), strings.Replace(string(body), `"iss":`, `"ISS":`, 1), strings.Replace(string(body), `"exp":200`, `"exp":200.5`, 1), strings.Replace(string(body), `"iss":"issuer"`, `"iss":"wrong","iss":"issuer"`, 1)} {
		if _, e = j.Verify(rawToken(t, j, `{"alg":"ES256","typ":"JWT"}`, bad), "access"); e == nil {
			t.Fatal("accepted malformed claims")
		}
	}
	for _, bad := range []JWT{{}, {Signer: LocalSigner{Key: &ecdsa.PrivateKey{}}}, {Signer: j.Signer, VerifyKey: j.VerifyKey}} {
		if _, e = bad.Sign(context.Background(), c); e == nil {
			t.Fatal("accepted invalid configuration")
		}
	}
	wrongCurve, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	j.Signer = LocalSigner{Key: wrongCurve}
	j.VerifyKey = &wrongCurve.PublicKey
	if _, e = j.Sign(context.Background(), c); e == nil {
		t.Fatal("accepted P384")
	}
}

func TestPassword(t *testing.T) {
	hash, e := HashPassword("correct horse")
	if e != nil {
		t.Fatal(e)
	}
	again, e := HashPassword("correct horse")
	if e != nil {
		t.Fatal(e)
	}
	if hash == again {
		t.Fatal("salt reused")
	}
	if !CheckPassword("correct horse", hash) || CheckPassword("wrong", hash) {
		t.Fatal("password check")
	}
	p := strings.Split(hash, "$")
	for _, bad := range []string{"", hash + "$extra", strings.Replace(hash, p[1], "999999999999", 1), strings.Replace(hash, p[1], "1", 1), strings.Replace(hash, p[2], "AA", 1), strings.Replace(hash, p[3], "AA", 1), strings.Replace(hash, "sha256", "sha1", 1)} {
		if CheckPassword("correct horse", bad) {
			t.Fatal("accepted bad hash")
		}
	}
	for _, bad := range []string{"", strings.Repeat("a", 4097)} {
		if _, e = HashPassword(bad); e == nil {
			t.Fatal("password bounds")
		}
		if CheckPassword(bad, hash) {
			t.Fatal("check bounds")
		}
	}
}

func TestSeal(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plain := []byte("secret")
	a, e := Seal(key, plain)
	if e != nil {
		t.Fatal(e)
	}
	b, e := Seal(key, plain)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonce reused")
	}
	got, e := Open(key, a)
	if e != nil || !bytes.Equal(got, plain) {
		t.Fatal("roundtrip", e)
	}
	a[len(a)-1] ^= 1
	for _, bad := range [][]byte{nil, {}, a, a[:10]} {
		if got, e = Open(key, bad); e == nil || got != nil {
			t.Fatal("accepted corruption")
		}
	}
	if _, e = Open(bytes.Repeat([]byte{8}, 32), b); e == nil {
		t.Fatal("accepted wrong key")
	}
	if _, e = Seal([]byte("bad"), plain); e == nil {
		t.Fatal("accepted bad key")
	}
	empty, e := Seal(key, nil)
	if e != nil {
		t.Fatal(e)
	}
	if got, e = Open(key, empty); e != nil || len(got) != 0 {
		t.Fatal("empty roundtrip")
	}
}

func TestPasswordKnownAnswer(t *testing.T) {
	// Independently generated with Python hashlib.pbkdf2_hmac, SHA-256, 600000 rounds.
	const encoded = "pbkdf2-sha256$600000$AAECAwQFBgcICQoLDA0ODw$O8NxGOYlCT6bee0Ikw6nr3OJWRIz_dkt3fNpNx5g28A"
	if !CheckPassword("password", encoded) {
		t.Fatal("PBKDF2 known answer mismatch")
	}
	if CheckPassword("password", strings.Replace(encoded, "AAEC", "AA\nEC", 1)) {
		t.Fatal("noncanonical salt accepted")
	}
}
