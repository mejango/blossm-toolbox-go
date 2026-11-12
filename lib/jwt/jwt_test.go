package token

import (
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

func testJWT(t *testing.T) (JWT[struct{}], Claims[struct{}]) {
	t.Helper()
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return JWT[struct{}]{Signer: LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "issuer", Audience: "network", Now: func() time.Time { return time.Unix(100, 0) }}, Claims[struct{}]{Issuer: "issuer", Audience: []string{"network"}, Subject: "subject", Expires: 200, NotBefore: 100, ID: "id", Kind: "access"}
}

func rawToken(t *testing.T, j JWT[struct{}], header, body string) string {
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
	for name, mutate := range map[string]func(*Claims[struct{}]){"expired": func(c *Claims[struct{}]) { c.Expires = 100 }, "future": func(c *Claims[struct{}]) { c.NotBefore = 101 }, "issuer": func(c *Claims[struct{}]) { c.Issuer = "other" }, "audience": func(c *Claims[struct{}]) { c.Audience = []string{"other"} }, "subject": func(c *Claims[struct{}]) { c.Subject = "" }, "id": func(c *Claims[struct{}]) { c.ID = "" }, "kind": func(c *Claims[struct{}]) { c.Kind = "refresh" }} {
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
	for _, bad := range []JWT[struct{}]{{}, {Signer: LocalSigner{Key: &ecdsa.PrivateKey{}}}, {Signer: j.Signer, VerifyKey: j.VerifyKey}} {
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

func TestTypedApplicationContext(t *testing.T) {
	type applicationContext struct {
		Tenant string `json:"tenant"`
	}
	base, _ := testJWT(t)
	jwt := JWT[applicationContext]{Signer: base.Signer, VerifyKey: base.VerifyKey, Issuer: base.Issuer, Audience: base.Audience, Now: base.Now}
	claims := Claims[applicationContext]{Issuer: base.Issuer, Audience: []string{base.Audience}, Subject: "actor", Expires: 200, NotBefore: 100, ID: "id", Kind: "access", Context: applicationContext{Tenant: "one"}}
	encoded, err := jwt.Sign(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := jwt.Verify(encoded, "access")
	if err != nil || got.Context.Tenant != "one" {
		t.Fatal(got, err)
	}
	body, _ := json.Marshal(claims)
	bad := strings.Replace(string(body), `"tenant":"one"`, `"tenant":"one","unrecognized":true`, 1)
	if _, err = jwt.Verify(rawToken(t, base, `{"alg":"ES256","typ":"JWT"}`, bad), "access"); err == nil {
		t.Fatal("accepted unknown application context field")
	}
	bad = strings.Replace(string(body), `"tenant":"one"`, `"tenant":"one","tenant":"two"`, 1)
	if _, err = jwt.Verify(rawToken(t, base, `{"alg":"ES256","typ":"JWT"}`, bad), "access"); err == nil {
		t.Fatal("accepted duplicate application context field")
	}
}
