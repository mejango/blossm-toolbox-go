# HTTP RPC

Standard-library transport for an explicit configured HTTP(S) base URL. No cloud
address discovery, token acquisition, queueing, automatic application retries or
response JSON decoding. Callers supply authentication headers and decode bytes.

```go
type Options struct {
    HTTP *http.Client
    Timeout time.Duration
    MaxBodyBytes int64
}
func New(baseURL string, options Options) (*Client, error)

type Response struct {
    StatusCode int
    Headers http.Header
    Body []byte
}
func (c *Client) Do(ctx context.Context, method, path string, query url.Values,
    body any, headers http.Header) (Response, error)
func (c *Client) Open(ctx context.Context, method, path string, query url.Values,
    body any, headers http.Header) (*http.Response, error)
```

Import `github.com/mejango/blossm-toolbox-go/rpc`. Zero options use `DefaultTimeout` (30 seconds)
and `DefaultMaxBodyBytes` (8 MiB); negative values are rejected. The byte bound
applies to response bytes exposed by the transport, including decompressed bytes
with the standard transport. Exactly the limit is allowed; reading beyond it
returns `ErrBodyTooLarge` (use `errors.Is`). Detection consumes at most one extra
probe byte. `Do` returns the bounded partial response alongside a read error.

GET, DELETE and HEAD require a nil body and use `url.Values` for query input.
POST, PUT and PATCH always JSON-encode the supplied value (`nil` means JSON
`null`) and set `Content-Type: application/json`. Other methods are rejected.
Headers and query inputs are not mutated. Paths are appended under the base
path, even with a leading slash. Absolute URLs, origin escapes, dot segments
(including repeatedly percent-encoded forms), backslashes, fragments and inline
queries are rejected. Supply queries separately. Base URLs must have a host,
HTTP(S) scheme and valid optional port, without credentials, query or fragment.

`Open` returns a stream only for 2xx responses. The caller must close its Body:

```go
response, err := client.Open(ctx, http.MethodGet, "facts", query, nil, headers)
if err != nil {
    return err
}
defer response.Body.Close()
// Pass response.Body to the fact decoder; reads enforce the same byte bound.
```

The timeout begins when the HTTP request is constructed and covers connection,
headers and the entire body, including pauses between stream reads. An earlier
parent context deadline or supplied client's timeout also applies. Closing the
body cancels the request context/timer immediately; a non-EOF read failure also
releases it. Clean EOF preserves the context and its deadline so buffered records
can finish their callbacks. Close the body after all callbacks, including after EOF. `response.Request` is explicitly set to the derived request;
stream callbacks should use `response.Request.Context()` to observe the same
deadline even while no read is in progress. JSON marshaling happens before the transport deadline.

Non-2xx responses, including redirects, return `*HTTPError` with `StatusCode` and
bounded `Body`. `Error()` prints only status, never URL, request headers or body.
Literal credentials from `Authorization`, `Proxy-Authorization`, `Cookie` and
`X-API-Key` headers (header names are case-insensitive), plus `token`,
`access_token` and `api_key` query fields, are redacted in Body. Other header
and query values are preserved, including IDs and intentional error metadata.
Body remains untrusted server data, not a general secret sanitizer. Do not log it as
safe diagnostic text. `Unwrap` preserves body limit/deadline/read errors so both
`errors.As(err, &httpError)` and `errors.Is(err, rpc.ErrBodyTooLarge)` work.
Transport errors omit net/http's URL wrapper to avoid exposing query credentials.

A supplied `http.Client` is copied, preserving its transport, jar and timeout,
while redirect following is always disabled. Do not mutate supplied client or
transport configuration concurrently. Standard `http.Transport` can retry safe
reads internally. Writes and DELETE carry a non-replayable body with no GetBody,
including when idempotency headers are supplied; RPC never retries them. DELETE
sends an empty chunked body to preserve this property. A failed write may already
have reached the server: recovery/idempotency decisions belong to the caller.
Custom transports/jars are trusted caller code and must honor cancellation and
the no-retry contract themselves.

Run real local HTTP tests without downloading dependencies:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test -race ./rpc
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go vet ./rpc
```

Tests cover wire query/JSON/auth semantics, exact body bounds, header and stream
timeouts, cancellation/Close, HTTP errors, redirects, invalid configuration and
paths, and disconnected side-effect calls on a reused standard transport.
