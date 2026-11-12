package gcp

import (
	"context"
	"fmt"
	"net/http"

	"golang.org/x/oauth2"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

type TokenSourceFactory func(context.Context, string, ...option.ClientOption) (oauth2.TokenSource, error)

// Auth creates audience-bound ID tokens through the official SDK. Construction
// is inert; credential discovery happens only on an explicit Token call.
type Auth struct {
	NewTokenSource TokenSourceFactory
	Options        []option.ClientOption
}

func (a Auth) Token(ctx context.Context, audience string) (string, error) {
	if ctx == nil {
		return "", fmt.Errorf("gcp: context required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if audience == "" {
		return "", fmt.Errorf("gcp: audience required")
	}
	factory := a.NewTokenSource
	if factory == nil {
		factory = idtoken.NewTokenSource
	}
	source, err := factory(ctx, audience, a.Options...)
	if err != nil {
		return "", err
	}
	if err := ready(ctx, source); err != nil {
		return "", err
	}
	token, err := source.Token()
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if token == nil || !token.Valid() {
		return "", fmt.Errorf("gcp: invalid ID token response")
	}
	return token.AccessToken, nil
}

// Authorize returns a cloned request. It never mutates a shared caller request.
func (a Auth) Authorize(req *http.Request, audience string) (*http.Request, error) {
	if req == nil {
		return nil, fmt.Errorf("gcp: request required")
	}
	token, err := a.Token(req.Context(), audience)
	if err != nil {
		return nil, err
	}
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+token)
	return copy, nil
}
