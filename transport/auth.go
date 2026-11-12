package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/security/authn"
	"github.com/mejango/blossm-toolbox-go/security/token"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"github.com/mejango/blossm-toolbox-go/stores/view"
)

type TokenVerifier func(context.Context, string) (Identity, error)

// Authenticator's verifier is bound by composition to this service/audience.
// Do not reuse a verifier for a different audience or interpret unverified JWTs.
// Optional accepts absent credentials only; invalid supplied credentials fail.
type Authenticator struct {
	Bearer   TokenVerifier
	Basic    func(context.Context, string, string) (Identity, error)
	Key      TokenVerifier
	Cookies  *CookiePolicy
	Optional bool
}

func (a Authenticator) Authenticate(r *http.Request) (Identity, error) {
	var zero Identity
	if err := r.Context().Err(); err != nil {
		return zero, err
	}
	headers := r.Header.Values("Authorization")
	if len(headers) > 1 {
		return zero, authn.ErrUnauthorized
	}
	keyValues := r.Header.Values("X-API-Key")
	if len(keyValues) > 1 {
		return zero, authn.ErrUnauthorized
	}
	hasHeader := len(headers) > 0
	hasKey := len(keyValues) > 0
	if (hasHeader && len(headers[0]) > 16384) || (hasKey && len(keyValues[0]) > 16384) {
		return zero, authn.ErrUnauthorized
	}
	cookieToken := ""
	if a.Cookies != nil {
		var err error
		cookieToken, err = a.Cookies.credential(r)
		if err != nil {
			return zero, err
		}
	}
	if (hasHeader && hasKey) || ((hasHeader || hasKey) && cookieToken != "") {
		return zero, authn.ErrUnauthorized
	}
	var identity Identity
	var err error
	switch {
	case hasHeader:
		scheme, value, ok := strings.Cut(headers[0], " ")
		if !ok || value == "" || strings.ContainsAny(value, " \t\r\n") {
			return zero, authn.ErrUnauthorized
		}
		switch {
		case strings.EqualFold(scheme, "Bearer") && a.Bearer != nil:
			identity, err = a.Bearer(r.Context(), value)
		case strings.EqualFold(scheme, "Basic") && a.Basic != nil:
			user, password, ok := r.BasicAuth()
			if !ok || user == "" || password == "" {
				return zero, authn.ErrUnauthorized
			}
			identity, err = a.Basic(r.Context(), user, password)
		default:
			return zero, authn.ErrUnauthorized
		}
	case hasKey:
		if a.Key == nil || keyValues[0] == "" || strings.TrimSpace(keyValues[0]) != keyValues[0] {
			return zero, authn.ErrUnauthorized
		}
		identity, err = a.Key(r.Context(), keyValues[0])
	case cookieToken != "":
		if a.Bearer == nil {
			return zero, authn.ErrUnauthorized
		}
		identity, err = a.Bearer(r.Context(), cookieToken)
	default:
		if a.Optional {
			return zero, nil
		}
		return zero, authn.ErrUnauthorized
	}
	if err != nil {
		return zero, authn.ErrUnauthorized
	}
	if err = r.Context().Err(); err != nil {
		return zero, err
	}
	// Detach verifier-owned maps/bytes before any route policy can mutate them.
	encoded, err := eventstore.Encode(identity)
	if err != nil {
		return zero, authn.ErrUnauthorized
	}
	var detached Identity
	if err = eventstore.Decode(encoded, &detached); err != nil {
		return zero, authn.ErrUnauthorized
	}
	var claims map[string]json.RawMessage
	if eventstore.Decode(detached.Claims, &claims) != nil || len(claims) == 0 {
		return zero, authn.ErrUnauthorized
	}
	return detached, nil
}

// GatewayRoute.Pattern is the registered ServeMux path pattern without a method,
// e.g. /commands/{name}. Name is the exact dynamic name, never a wildcard.
// Operation is empty for Framework, or the View/Projection operation string.
type GatewayRoute struct{ Method, Pattern, Name, Operation string }
type GatewayRequest struct {
	Request                 *http.Request
	Operation, Name, RootID string
}
type GatewayCheck func(context.Context, Identity, GatewayRequest) error

type GatewayPolicy struct {
	RequiredContexts []string
	AllowedContexts  map[string][]eventstore.Root
	Check            GatewayCheck
	Scope            func(context.Context, Identity, GatewayRequest) (view.Scope, error)
	Projection       func(context.Context, Identity, GatewayRequest) (ProjectionBinding, error)
}
type GatewayAuthority struct {
	auth   Authenticator
	routes map[GatewayRoute]GatewayPolicy
}

func NewGatewayAuthority(auth Authenticator, routes map[GatewayRoute]GatewayPolicy) (*GatewayAuthority, error) {
	if len(routes) == 0 || (!auth.Optional && auth.Bearer == nil && auth.Basic == nil && auth.Key == nil) {
		return nil, errors.New("gateway authentication and explicit routes required")
	}
	if auth.Cookies != nil {
		if err := auth.Cookies.validate(); err != nil {
			return nil, err
		}
		copy := *auth.Cookies
		copy.Types = append([]string(nil), copy.Types...)
		copy.Origins = append([]string(nil), copy.Origins...)
		auth.Cookies = &copy
	}
	a := &GatewayAuthority{auth: auth, routes: make(map[GatewayRoute]GatewayPolicy, len(routes))}
	for route, p := range routes {
		if route.Method == "" || route.Pattern == "" || route.Pattern[0] != '/' || p.Check == nil || strings.ContainsAny(route.Method, " \t\r\n") || strings.ContainsAny(route.Pattern, " \t\r\n") {
			return nil, errors.New("exact method/path and authority check required")
		}
		if strings.Contains(route.Pattern, "{name}") && route.Name == "" {
			return nil, errors.New("named route requires exact name")
		}
		p.RequiredContexts = append([]string(nil), p.RequiredContexts...)
		allowed := make(map[string][]eventstore.Root, len(p.AllowedContexts))
		for name, refs := range p.AllowedContexts {
			if name == "" || len(refs) == 0 {
				return nil, errors.New("empty context protection")
			}
			for _, ref := range refs {
				if ref.Network == "" || ref.Service == "" || ref.Domain == "" || ref.ID == "" {
					return nil, errors.New("complete protected context required")
				}
			}
			allowed[name] = append([]eventstore.Root(nil), refs...)
		}
		p.AllowedContexts = allowed
		a.routes[route] = p
	}
	return a, nil
}
func (a *GatewayAuthority) authorize(r *http.Request, op, name, rootID string) (Identity, GatewayPolicy, error) {
	var zero Identity
	pattern := r.Pattern
	if _, path, ok := strings.Cut(pattern, " "); ok {
		pattern = path
	}
	if name != r.PathValue("name") {
		return zero, GatewayPolicy{}, authn.ErrForbidden
	}
	policy, ok := a.routes[GatewayRoute{Method: r.Method, Pattern: pattern, Name: name, Operation: op}]
	if !ok {
		return zero, policy, authn.ErrForbidden
	}
	identity, err := a.auth.Authenticate(r)
	if err != nil {
		return zero, policy, err
	}
	if err = command.RequireContext(identity.Context, policy.RequiredContexts); err != nil {
		return zero, policy, authn.ErrForbidden
	}
	for name, refs := range policy.AllowedContexts {
		var actual eventstore.Root
		if eventstore.Decode(identity.Context[name], &actual) != nil {
			return zero, policy, authn.ErrForbidden
		}
		matched := false
		for _, ref := range refs {
			matched = matched || ref == actual
		}
		if !matched {
			return zero, policy, authn.ErrForbidden
		}
	}
	if err = policy.Check(r.Context(), identity, GatewayRequest{r, op, name, rootID}); err != nil {
		return zero, policy, err
	}
	return identity, policy, nil
}
func (a *GatewayAuthority) Framework(r *http.Request) (Identity, error) {
	identity, _, err := a.authorize(r, "", r.PathValue("name"), "")
	return identity, err
}
func (a *GatewayAuthority) View(r *http.Request, operation, name string) (view.Scope, error) {
	identity, p, err := a.authorize(r, operation, name, r.PathValue("id"))
	if err != nil {
		return view.Scope{}, err
	}
	if p.Scope == nil {
		return view.Scope{}, authn.ErrForbidden
	}
	return p.Scope(r.Context(), identity, GatewayRequest{r, operation, name, r.PathValue("id")})
}
func (a *GatewayAuthority) Projection(r *http.Request, op ProjectionOperation) (ProjectionBinding, error) {
	operation := "play"
	if op.Replay {
		operation = "replay"
	}
	identity, p, err := a.authorize(r, operation, op.Name, op.RootID)
	if err != nil {
		return ProjectionBinding{}, err
	}
	if p.Projection == nil {
		return ProjectionBinding{}, authn.ErrForbidden
	}
	return p.Projection(r.Context(), identity, GatewayRequest{r, operation, op.Name, op.RootID})
}

// ServiceJWTVerifier pins issuer/audience/key through JWT and requires the exact
// configured peer subject and service token kind. It never accepts an access
// token as an internal service credential.
func ServiceJWTVerifier[C any](jwt token.JWT[C], subject string) (TokenVerifier, error) {
	if subject == "" || jwt.VerifyKey == nil || jwt.Issuer == "" || jwt.Audience == "" {
		return nil, errors.New("pinned service JWT identity required")
	}
	return func(ctx context.Context, token string) (Identity, error) {
		if err := ctx.Err(); err != nil {
			return Identity{}, err
		}
		c, err := jwt.Verify(token, "service")
		if err != nil || c.Subject != subject {
			return Identity{}, authn.ErrUnauthorized
		}
		return IdentityFromClaims(c)
	}, nil
}
func IdentityFromClaims[C any](c token.Claims[C]) (Identity, error) {
	raw, err := eventstore.Encode(c.Context)
	if err != nil {
		return Identity{}, err
	}
	var values command.Context
	if err = eventstore.Decode(raw, &values); err != nil {
		return Identity{}, err
	}
	claims, err := eventstore.Encode(c)
	return Identity{Context: values, Claims: claims}, err
}

// InspectGatewayJSON lets a route policy authorize a typed body resource using
// the same strict bounded decoder as execution, while restoring bytes for the
// procedure. Decode the complete owning request type, not a permissive subset.
func InspectGatewayJSON(r *http.Request, dst any) error {
	if r.Body == nil {
		return authn.ErrInvalid
	}
	original := r.Body
	var captured bytes.Buffer
	r.Body = io.NopCloser(io.TeeReader(original, &captured))
	err := Decode(nil, r, dst)
	r.Body = io.NopCloser(bytes.NewReader(captured.Bytes()))
	_ = original.Close()
	return err
}
