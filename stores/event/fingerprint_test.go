package eventstore

import (
	"encoding/json"
	"testing"
)

func TestRequestFingerprintSeparatesTokenLifetimeFromAuthority(t *testing.T) {
	original := json.RawMessage(`{"sub":"alice","iss":"issuer","aud":"api","exp":100,"iat":1,"nbf":1,"jti":"old","role":"writer"}`)
	in := Input{Transaction: Transaction{Claims: original}}
	want, err := requestFingerprint(in)
	if err != nil {
		t.Fatal(err)
	}
	in.Transaction.Claims = json.RawMessage(`{"sub":"alice","iss":"issuer","aud":"api","exp":200,"iat":2,"nbf":2,"jti":"new","role":"writer"}`)
	got, err := requestFingerprint(in)
	if err != nil || got != want {
		t.Fatal("refreshed credential changed request identity", err)
	}
	for _, key := range []string{"sub", "iss", "aud", "role"} {
		t.Run(key, func(t *testing.T) {
			var claims map[string]json.RawMessage
			if err := json.Unmarshal(original, &claims); err != nil {
				t.Fatal(err)
			}
			claims[key] = json.RawMessage(`"different"`)
			changed := in
			changed.Transaction.Claims, _ = json.Marshal(claims)
			got, err := requestFingerprint(changed)
			if err != nil || got == want {
				t.Fatal("changed authority reused request identity", err)
			}
		})
	}
	if string(original) != `{"sub":"alice","iss":"issuer","aud":"api","exp":100,"iat":1,"nbf":1,"jti":"old","role":"writer"}` {
		t.Fatal("fingerprint mutated committed evidence")
	}
}

func TestEventHashStillBindsVolatileClaims(t *testing.T) {
	event := Event{Input: Input{Transaction: Transaction{Claims: json.RawMessage(`{"sub":"alice","exp":100,"jti":"original"}`)}}}
	var err error
	event.Fingerprint, err = requestFingerprint(event.Input)
	if err != nil {
		t.Fatal(err)
	}
	event.Hash, err = Digest(event)
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(event); err != nil {
		t.Fatal(err)
	}
	event.Input.Transaction.Claims = json.RawMessage(`{"sub":"alice","exp":200,"jti":"tampered"}`)
	if err = Verify(event); err == nil {
		t.Fatal("accepted modified original token evidence")
	}
}
