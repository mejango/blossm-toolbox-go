package aws

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

// ServiceAuth signs AWS IAM-authenticated HTTP requests, not bearer tokens.
// Credentials must be explicitly injected; this package never discovers them.
type ServiceAuth struct {
	Credentials sdk.CredentialsProvider
	Region      string
	Service     string
}

// Sign returns a signed clone. A body requires GetBody so signing cannot consume
// the outgoing stream. The caller owns sending/closing the returned request.
// Re-sign each retry. Do not forward signed requests to another host.
func (a ServiceAuth) Sign(ctx context.Context, req *http.Request, at time.Time) (*http.Request, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil || req.URL == nil || req.URL.Host == "" || (req.URL.Scheme != "https" && req.URL.Scheme != "http") || req.URL.User != nil || a.Credentials == nil || a.Region == "" || a.Service == "" || at.IsZero() {
		return nil, errors.New("aws sigv4: request, credentials, region, service and signing time required")
	}
	signed := req.Clone(ctx)
	hash := sha256.New()
	if req.Body != nil && req.Body != http.NoBody {
		if req.GetBody == nil {
			return nil, errors.New("aws sigv4: body must support GetBody")
		}
		body, err := req.GetBody()
		if err != nil {
			if body != nil {
				_ = body.Close()
			}
			return nil, err
		}
		if body == nil {
			return nil, errors.New("aws sigv4: GetBody returned nil")
		}
		_, err = io.Copy(hash, contextReader{ctx: ctx, reader: body})
		closeErr := body.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		signed.Body, err = req.GetBody()
		if err != nil {
			if signed.Body != nil {
				_ = signed.Body.Close()
			}
			return nil, err
		}
		if signed.Body == nil {
			return nil, errors.New("aws sigv4: GetBody returned nil")
		}
	}
	credentials, err := a.Credentials.Retrieve(ctx)
	if err == nil && (!credentials.HasKeys() || (credentials.CanExpire && !credentials.Expires.After(at))) {
		err = errors.New("aws sigv4: credentials missing or expired")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = v4.NewSigner().SignHTTP(ctx, credentials, signed, hex.EncodeToString(hash.Sum(nil)), a.Service, a.Region, at)
	}
	if err != nil {
		if signed.Body != nil && signed.Body != req.Body {
			_ = signed.Body.Close()
		}
		return nil, err
	}
	return signed, nil
}

// contextReader checks cancellation between body reads. GetBody streams must
// themselves honor cancellation if a single Read can block indefinitely.
type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
