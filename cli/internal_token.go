package cli

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	"blossm.local/backend/dia"
	"blossm.local/backend/runtimeauth"
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
	if c.Runtime != "framework" || c.InternalSubject == "" {
		return errors.New("internal-token requires framework internalSubject configuration")
	}
	if !*execute {
		return printPlan(out, map[string]any{"status": "prepared-only", "operation": "internal-token", "subject": c.InternalSubject, "audience": c.Network, "issuer": "dia." + c.Network, "signer": c.Signing.Provider, "ttl": ttl.String(), "note": "Explicit execution loads configured signer environment and prints a sensitive service JWT. No keys are fetched or signing performed during preparation. This is not a Google invocation or delegated-user credential."})
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.getenv("JWT_SIGNER") != c.Signing.Provider || e.getenv("JWT_KMS_KEY") != c.Signing.KMSKey {
		return errors.New("signer environment must match configured signing provider and key")
	}
	open := e.OpenJWT
	if open == nil {
		open = runtimeauth.Open
	}
	jwt, close, err := open(ctx, c.Network)
	if close != nil {
		defer close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("internal signer initialization failed")
	}
	if jwt == nil || close == nil || jwt.Issuer != "dia."+c.Network || jwt.Audience != c.Network {
		return errors.New("internal signer returned inconsistent configured trust")
	}
	now := time.Now()
	token, err := jwt.Sign(ctx, dia.Claims{Issuer: jwt.Issuer, Audience: []string{c.Network}, Subject: c.InternalSubject, Expires: now.Add(*ttl).Unix(), NotBefore: now.Unix(), ID: rand.Text(), Kind: "service"})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("internal token signing failed")
	}
	_, err = fmt.Fprintln(out, token)
	return err
}
