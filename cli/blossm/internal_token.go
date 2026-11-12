package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/mejango/blossm-toolbox-go/lib/jwt"
	command "github.com/mejango/blossm-toolbox-go/procedures/command"
	runtimeauth "github.com/mejango/blossm-toolbox-go/procedures/server/auth"
)

// internalToken issues application service authority, not Google invocation or
// delegated-user authority. Secret values are loaded only by explicit execution.
func internalToken(ctx context.Context, args []string, out, stderr io.Writer, e Execution) error {
	f := flag.NewFlagSet("internal-token", flag.ContinueOnError)
	f.SetOutput(stderr)
	path := f.String("config", "blossm.json", "framework config with exact internal subject/network")
	ttl := f.Duration("ttl", 5*time.Minute, "token lifetime, 1s through 5m")
	execute := f.Bool("execute", false, "sign and print a sensitive internal service JWT")
	dry := f.Bool("dry-run", false, "prepare offline only (default)")
	if err := parse(f, args); err != nil {
		return err
	}
	if *execute && *dry {
		return errors.New("execute and dry-run are mutually exclusive")
	}
	if *ttl < time.Second || *ttl > 5*time.Minute {
		return errors.New("internal token ttl must be1s..5m")
	}
	c, err := ReadConfig(*path)
	if err != nil {
		return err
	}
	if c.Signing == nil || c.Signing.Issuer == "" || c.Signing.Audience == "" || c.Signing.Subject == "" {
		return errors.New("internal-token requires explicit signing issuer, audience and subject")
	}
	if !*execute {
		return printPlan(out, map[string]any{"status": "prepared-only", "operation": "internal-token", "subject": c.Signing.Subject, "audience": c.Signing.Audience, "issuer": c.Signing.Issuer, "signer": c.Signing.Provider, "ttl": ttl.String(), "note": "Explicit execution loads configured signer environment and prints a sensitive service JWT. No keys are fetched or signing performed during preparation. This is not a Google invocation or delegated-user credential."})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.getenv("JWT_SIGNER") != c.Signing.Provider || e.getenv("JWT_KMS_KEY") != c.Signing.KMSKey {
		return errors.New("signer environment must match configured signing provider and key")
	}
	open := e.OpenJWT
	if open == nil {
		open = runtimeauth.Open[command.Context]
	}
	jwt, close, err := open(ctx, c.Signing.Issuer, c.Signing.Audience)
	if close != nil {
		defer close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("internal signer initialization failed")
	}
	if jwt == nil || close == nil || jwt.Issuer != c.Signing.Issuer || jwt.Audience != c.Signing.Audience {
		return errors.New("internal signer returned inconsistent configured trust")
	}
	now := time.Now()
	token, err := jwt.Sign(ctx, token.Claims[command.Context]{Issuer: jwt.Issuer, Audience: []string{c.Signing.Audience}, Subject: c.Signing.Subject, Expires: now.Add(*ttl).Unix(), NotBefore: now.Unix(), ID: rand.Text(), Kind: "service", Context: command.Context{}})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("internal token signing failed")
	}
	_, err = fmt.Fprintln(out, token)
	return err
}
