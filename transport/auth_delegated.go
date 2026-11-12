package transport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"time"

	"blossm.local/backend/command"
	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
)

// DelegatedCommandAuthorizer is installed on a CommandClient used inside an
// authenticated Main. parent must be the Framework-authenticated request, never
// raw client JSON. jwt is the issuing peer's signer with the target service's
// exact audience; peerSubject identifies that peer, not the delegated user.
// Delegation keeps user context and never outlives its parent credential.
func DelegatedCommandAuthorizer(jwt dia.JWT, identity *dia.Service, peerSubject string) (func(context.Context, command.Request) (http.Header, error), error) {
	if identity == nil || jwt.Signer == nil || jwt.VerifyKey == nil || jwt.Issuer == "" || jwt.Audience == "" || peerSubject == "" {
		return nil, errors.New("delegation signer, target audience, peer and Dia state validator required")
	}
	return func(ctx context.Context, parent command.Request) (http.Header, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var c dia.Claims
		if eventstore.Decode(parent.Claims, &c) != nil || (c.Kind != "access" && c.Kind != "service") {
			return nil, dia.ErrUnauthorized
		}
		// The procedure's authenticated context and its audit claims must agree.
		// Prevent application code accidentally substituting a body-selected actor.
		claimContext, err := eventstore.Encode(c.Context)
		if err != nil {
			return nil, err
		}
		actualContext, err := eventstore.Encode(parent.Context)
		if err != nil || !bytes.Equal(claimContext, actualContext) {
			return nil, dia.ErrUnauthorized
		}
		if err = identity.AuthorizeDelegatedContext(ctx, c); err != nil {
			return nil, err
		}
		now := time.Now()
		if jwt.Now != nil {
			now = jwt.Now()
		}
		expires := min(c.Expires, now.Add(time.Minute).Unix())
		if c.NotBefore > now.Unix() || expires <= now.Unix() {
			return nil, dia.ErrUnauthorized
		}
		transaction := parent.Transaction
		if transaction.ID == "" {
			transaction.ID = parent.ID
		}
		if transaction.Issued == "" {
			transaction.Issued = parent.Issued
		}
		binding, err := delegatedTransactionID(transaction)
		if err != nil {
			return nil, err
		}
		delegated := dia.Claims{Issuer: jwt.Issuer, Audience: []string{jwt.Audience}, Subject: peerSubject, ID: binding, Kind: "service", Context: c.Context, NotBefore: now.Unix(), Expires: expires}
		token, err := jwt.Sign(ctx, delegated)
		if err != nil {
			return nil, err
		}
		return http.Header{"Authorization": {"Bearer " + token}}, nil
	}, nil
}

// DelegatedCommandVerifier first authenticates the configured peer, then asks
// the Dia owner to validate the CURRENT delegated user's session/scene state.
// Use DelegatedCommandCheck as the route policy too, or DelegatedCommandGateway,
// to bind the credential to the forwarded parent transaction before execution.
func DelegatedCommandVerifier(jwt dia.JWT, identity *dia.Service, peerSubject string) (TokenVerifier, error) {
	if identity == nil {
		return nil, errors.New("delegated Dia state validator required")
	}
	verify, err := ServiceJWTVerifier(jwt, peerSubject)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, token string) (Identity, error) {
		verified, err := verify(ctx, token)
		if err != nil {
			return Identity{}, err
		}
		var claims dia.Claims
		if eventstore.Decode(verified.Claims, &claims) != nil || len(claims.Audience) != 1 || claims.Audience[0] != jwt.Audience {
			return Identity{}, dia.ErrUnauthorized
		}
		// A generic peer token is not sufficient: it must carry the bound delegated
		// transaction marker and a current end-user session owned by this network.
		if len(claims.ID) != len(delegatedIDPrefix)+64 || claims.ID[:len(delegatedIDPrefix)] != delegatedIDPrefix {
			return Identity{}, dia.ErrUnauthorized
		}
		if err = identity.AuthorizeDelegatedContext(ctx, claims); err != nil {
			return Identity{}, err
		}
		return verified, nil
	}, nil
}

// DelegatedCommandCheck is a mandatory companion to the token-only verifier.
// Compare the original parent trace before Command.Execute appends the receiving
// procedure's trace; body claims/context remain ignored by Framework.
func DelegatedCommandCheck(ctx context.Context, identity Identity, request GatewayRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r := request.Request
	if r == nil || r.Method != "POST" || r.Pattern != "POST /commands/{name}" || r.PathValue("name") == "" {
		return dia.ErrForbidden
	}
	var claims dia.Claims
	if eventstore.Decode(identity.Claims, &claims) != nil || claims.Kind != "service" {
		return dia.ErrUnauthorized
	}
	var in command.Request
	if err := InspectGatewayJSON(r, &in); err != nil {
		return err
	}
	binding, err := delegatedTransactionID(in.Transaction)
	if err != nil || binding != claims.ID {
		return dia.ErrForbidden
	}
	return nil
}

// DelegatedCommandGateway makes token verification, current user-state checking
// and transaction binding inseparable. The caller still selects exact allowed
// command names and enforces command/resource permissions at its route owner.
// It must never be installed as a delivery-envelope or store-peer authenticator.
func DelegatedCommandGateway(jwt dia.JWT, identity *dia.Service, peerSubject string) (func(*http.Request) (Identity, error), error) {
	verifier, err := DelegatedCommandVerifier(jwt, identity, peerSubject)
	if err != nil {
		return nil, err
	}
	authenticate := Authenticator{Bearer: verifier}
	return func(r *http.Request) (Identity, error) {
		verified, err := authenticate.Authenticate(r)
		if err != nil {
			return Identity{}, err
		}
		if err = DelegatedCommandCheck(r.Context(), verified, GatewayRequest{Request: r, Name: r.PathValue("name")}); err != nil {
			return Identity{}, err
		}
		return verified, nil
	}, nil
}

const delegatedIDPrefix = "command-delegation-v1:"

func delegatedTransactionID(transaction eventstore.Transaction) (string, error) {
	if transaction.ID == "" || len(transaction.ID) > 1024 || transaction.Issued == "" {
		return "", dia.ErrInvalid
	}
	issued, err := time.Parse(time.RFC3339Nano, transaction.Issued)
	if err != nil {
		return "", dia.ErrInvalid
	}
	for _, trace := range transaction.Path {
		if trace.Network == "" || trace.Host == "" || trace.Procedure == "" || trace.Hash == "" {
			return "", dia.ErrInvalid
		}
	}
	// Claims are authenticated separately and are not forwarded by CommandClient.
	// Nil and empty trace slices represent the same empty parent trace.
	path := append([]eventstore.Trace{}, transaction.Path...)
	digest, err := eventstore.Digest(struct {
		Domain        string             `json:"domain"`
		TransactionID string             `json:"transactionId"`
		Issued        string             `json:"issued"`
		Path          []eventstore.Trace `json:"path"`
	}{delegatedIDPrefix, transaction.ID, issued.UTC().Format(time.RFC3339Nano), path})
	if err != nil {
		return "", err
	}
	return delegatedIDPrefix + digest, nil
}
