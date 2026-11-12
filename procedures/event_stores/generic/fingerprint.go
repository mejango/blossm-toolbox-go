package eventstore

import (
	"encoding/json"
	"fmt"

	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
)

// requestFingerprint identifies the authenticated operation, not the lifetime of
// the credential used to retry it. Only standard token issuance/lifetime fields
// are omitted. Subject, issuer, audience, permissions, session context and any
// custom claims remain bound. The event hash still commits the original claims.
func requestFingerprint(in eventlib.Input) (string, error) {
	if len(in.Transaction.Claims) > 0 {
		var claims map[string]json.RawMessage
		if err := canonicaljson.Decode(in.Transaction.Claims, &claims); err != nil {
			return "", err
		}
		if claims == nil {
			return "", fmt.Errorf("claims must be object")
		}
		for _, name := range []string{"exp", "iat", "nbf", "jti"} {
			delete(claims, name)
		}
		var err error
		in.Transaction.Claims, err = canonicaljson.Encode(claims)
		if err != nil {
			return "", err
		}
	}
	return canonicaljson.Digest(in)
}
