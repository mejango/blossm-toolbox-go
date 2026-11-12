package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"blossm.local/backend/dia"
	"blossm.local/backend/rpc"
)

// FactProxy forwards only to a composition-selected RPC target/path. Credentials
// are minted/selected by Upstream from verified identity, never copied from the
// incoming Authorization/Cookie headers. Query selectors are not authority.
type FactProxy struct {
	Authorize func(*http.Request) (Identity, error)
	RPC       *rpc.Client
	Path      string
	Upstream  func(context.Context, Identity) (http.Header, error)
}

func (p FactProxy) Handler() (http.Handler, error) {
	if p.Authorize == nil || p.RPC == nil || p.Path == "" || p.Upstream == nil {
		return nil, errors.New("fact proxy requires authorization, fixed RPC target/path and upstream credentials")
	}
	return wrap(func(w http.ResponseWriter, r *http.Request) error {
		if r.Method != "GET" {
			w.WriteHeader(405)
			return nil
		}
		identity, err := p.Authorize(r)
		if err != nil {
			return err
		}
		if len(r.URL.RawQuery) > 64<<10 {
			return dia.ErrInvalid
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return dia.ErrInvalid
		}
		headers, err := p.Upstream(r.Context(), identity)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		response, err := p.RPC.Open(ctx, "GET", p.Path, query, nil, headers)
		if err != nil {
			var remote *rpc.HTTPError
			if errors.As(err, &remote) {
				switch remote.StatusCode {
				case 400, 401, 403, 404, 409, 429:
					return write(w, remote.StatusCode, map[string]string{"error": "upstream request rejected"})
				}
			}
			return err
		}
		defer response.Body.Close()
		forwarded := forwardGatewayHeaders(response.Header)
		// Read before committing headers so a pre-body read error is still an error
		// response. Once started, any truncation/error aborts the downstream stream.
		buffer := make([]byte, 32<<10)
		started := false
		for {
			n, readErr := response.Body.Read(buffer)
			if n > 0 {
				if !started {
					for k, values := range forwarded {
						w.Header()[k] = values
					}
					w.WriteHeader(response.StatusCode)
					started = true
				}
				if _, err = w.Write(buffer[:n]); err != nil {
					cancel()
					panic(http.ErrAbortHandler)
				}
				if err = http.NewResponseController(w).Flush(); err != nil {
					cancel()
					panic(http.ErrAbortHandler)
				}
			}
			if errors.Is(readErr, io.EOF) {
				if !started {
					for k, values := range forwarded {
						w.Header()[k] = values
					}
					w.WriteHeader(response.StatusCode)
				}
				return nil
			}
			if readErr != nil {
				if started {
					panic(http.ErrAbortHandler)
				}
				return readErr
			}
			if err = ctx.Err(); err != nil {
				if started {
					panic(http.ErrAbortHandler)
				}
				return err
			}
		}
	}), nil
}
func forwardGatewayHeaders(headers http.Header) http.Header {
	out := headers.Clone()
	for _, connection := range headers.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			out.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Content-Length", "Set-Cookie"} {
		out.Del(name)
	}
	return out
}
