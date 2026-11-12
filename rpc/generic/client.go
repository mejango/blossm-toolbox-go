// Package rpc provides bounded HTTP calls to explicitly configured services.
package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const DefaultTimeout = 30 * time.Second
const DefaultMaxBodyBytes int64 = 8 << 20

var ErrBodyTooLarge = errors.New("rpc: response body exceeds limit")

type Options struct {
	HTTP         *http.Client
	Timeout      time.Duration
	MaxBodyBytes int64
}

type Response struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// HTTPError contains bounded response bytes, with echoed request credentials
// redacted. Body is untrusted server data; Error deliberately prints only status.
type HTTPError struct {
	StatusCode int
	Body       []byte
	cause      error
}

func (e *HTTPError) Error() string { return fmt.Sprintf("rpc: HTTP status %d", e.StatusCode) }
func (e *HTTPError) Unwrap() error { return e.cause }

type Client struct {
	base    url.URL
	http    http.Client
	timeout time.Duration
	maxBody int64
}

func New(baseURL string, options Options) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.Contains(baseURL, "#") || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery {
		return nil, errors.New("rpc: invalid base URL")
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, errors.New("rpc: invalid base port")
		}
	}
	if strings.HasSuffix(u.Host, ":") || unsafePath(u.EscapedPath()) {
		return nil, errors.New("rpc: invalid base path or host")
	}
	if options.Timeout < 0 || options.MaxBodyBytes < 0 {
		return nil, errors.New("rpc: negative timeout or body limit")
	}
	if options.Timeout == 0 {
		options.Timeout = DefaultTimeout
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = DefaultMaxBodyBytes
	}
	c := &Client{base: *u, timeout: options.Timeout, maxBody: options.MaxBodyBytes}
	if options.HTTP != nil {
		c.http = *options.HTTP
	}
	c.http.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, nil
}

// unsafePath rejects traversal even after repeated URL decoding by intermediaries.
func unsafePath(p string) bool {
	for {
		if strings.ContainsAny(p, "\\\x00\r\n") || strings.HasPrefix(p, "//") {
			return true
		}
		for _, part := range strings.Split(p, "/") {
			if part == "." || part == ".." {
				return true
			}
		}
		next, err := url.PathUnescape(p)
		if err != nil {
			return true
		}
		if next == p {
			return false
		}
		p = next
	}
}

// Open returns only successful (2xx) responses. The caller must close Body,
// which cancels the request deadline. The deadline covers the entire stream.
func (c *Client) Open(ctx context.Context, method, path string, query url.Values, body any, headers http.Header) (*http.Response, error) {
	if ctx == nil {
		return nil, errors.New("rpc: nil context")
	}
	switch method {
	case http.MethodGet, http.MethodDelete, http.MethodHead:
		if body != nil {
			return nil, errors.New("rpc: read/delete body must be nil; use query")
		}
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return nil, errors.New("rpc: unsupported method")
	}
	p, err := url.Parse(path)
	if err != nil || p.IsAbs() || p.Host != "" || p.User != nil || p.Opaque != "" || strings.ContainsAny(path, "?#") || unsafePath(path) {
		return nil, errors.New("rpc: invalid request path")
	}
	u := c.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + strings.TrimLeft(p.Path, "/")
	u.RawPath = strings.TrimRight(c.base.EscapedPath(), "/") + "/" + strings.TrimLeft(p.EscapedPath(), "/")
	u.RawQuery = query.Encode()
	var reader io.Reader
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		data, e := json.Marshal(body)
		if e != nil {
			return nil, errors.New("rpc: body is not JSON encodable")
		}
		reader = strings.NewReader(string(data))
	}
	// Hide replayable reader types, including an empty DELETE body, from net/http.
	// Nonzero/unknown outgoing length plus nil GetBody prevents transport replay.
	if method == http.MethodDelete {
		reader = strings.NewReader("")
	}
	if reader != nil {
		reader = struct{ io.Reader }{reader}
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.timeout)
	req, err := http.NewRequestWithContext(requestCtx, method, u.String(), reader)
	if err != nil {
		cancel()
		return nil, errors.New("rpc: invalid request")
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		// net/http's URL wrapper can contain query credentials.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	resp.Request = req
	resp.Body = &boundedBody{body: resp.Body, remaining: c.maxBody, cancel: cancel}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		safeBody := redact(data, req)
		if int64(len(safeBody)) > c.maxBody {
			safeBody = safeBody[:c.maxBody]
		}
		return nil, &HTTPError{StatusCode: resp.StatusCode, Body: safeBody, cause: readErr}
	}
	return resp, nil
}

func (c *Client) Do(ctx context.Context, method, path string, query url.Values, body any, headers http.Header) (Response, error) {
	resp, err := c.Open(ctx, method, path, query, body, headers)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	return Response{StatusCode: resp.StatusCode, Headers: resp.Header.Clone(), Body: data}, err
}

func redact(data []byte, req *http.Request) []byte {
	text := string(data)
	secrets := []string{}
	for name, values := range req.Header {
		switch strings.ToLower(name) {
		case "authorization", "proxy-authorization":
			for _, value := range values {
				secrets = append(secrets, value)
				// The authentication scheme is public; only its credentials are secret.
				fields := strings.Fields(value)
				if len(fields) > 1 {
					secrets = append(secrets, fields[1:]...)
				}
			}
		case "x-api-key":
			secrets = append(secrets, values...)
		case "cookie":
			secrets = append(secrets, values...)
			cookieRequest := http.Request{Header: http.Header{"Cookie": values}}
			for _, cookie := range cookieRequest.Cookies() {
				secrets = append(secrets, cookie.Value)
			}
		}
	}
	query := req.URL.Query()
	for _, name := range []string{"token", "access_token", "api_key"} {
		secrets = append(secrets, query[name]...)
	}
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return []byte(text)
}

type boundedBody struct {
	body      io.ReadCloser
	remaining int64
	cancel    context.CancelFunc
	once      sync.Once
	terminal  error
}

func (b *boundedBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.terminal != nil {
		return 0, b.terminal
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			err = ErrBodyTooLarge
		}
		if err != nil {
			b.terminal = err
			if err != io.EOF {
				b.Close()
			}
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	if err != nil {
		b.terminal = err
		// EOF ends network reads, but buffered consumer callbacks may remain.
		if err != io.EOF {
			b.Close()
		}
	}
	return n, err
}
func (b *boundedBody) Close() error {
	b.cancel()
	var err error
	b.once.Do(func() { err = b.body.Close() })
	return err
}
