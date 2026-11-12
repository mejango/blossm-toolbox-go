// Package httpwire owns bounded, credential-safe HTTP transport for external adapters.
package httpwire

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ResponseLimit = 4 << 20

// StatusError intentionally excludes provider body, URL and credentials.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("external service returned HTTP %d", e.Status)
}

type Client struct {
	base string
	http *http.Client
}

func New(base string, h *http.Client) (*Client, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid external service endpoint")
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && local) {
		return nil, errors.New("external service requires HTTPS (HTTP allowed only on loopback)")
	}
	var client http.Client
	if h != nil {
		client = *h
	}
	if client.Timeout == 0 {
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{base: strings.TrimRight(base, "/"), http: &client}, nil
}
func (c *Client) Endpoint() string { return c.base }
func (c *Client) Do(ctx context.Context, method, path string, headers http.Header, body io.Reader) ([]byte, http.Header, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "?#\\") {
		return nil, nil, errors.New("invalid external service path")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, nil, errors.New("cannot construct external request")
	}
	req.Header = headers.Clone()
	// Prevent net/http from replaying side effects after a lost connection, even
	// when the caller provides an Idempotency-Key. Recovery belongs to the caller.
	if method != http.MethodGet && method != http.MethodHead {
		req.GetBody = nil
		if req.Body == nil || req.Body == http.NoBody {
			req.Body = io.NopCloser(strings.NewReader(""))
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("external service transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, &StatusError{resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, ResponseLimit+1))
	if err != nil {
		return nil, nil, errors.New("external service response read failed")
	}
	if len(b) > ResponseLimit {
		return nil, nil, errors.New("external service response exceeded limit")
	}
	return b, resp.Header, nil
}
