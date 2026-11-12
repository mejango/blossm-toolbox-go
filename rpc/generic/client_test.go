package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func client(t *testing.T, s *httptest.Server, o Options) *Client {
	t.Helper()
	c, err := New(s.URL+"/api", o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWire(t *testing.T) {
	for _, method := range []string{"GET", "DELETE", "POST", "PUT", "PATCH", "HEAD"} {
		t.Run(method, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.URL.Path != "/api/items/a b" || r.URL.Query().Get("q") != "a & b" || len(r.URL.Query()["tag"]) != 2 {
					t.Errorf("wrong request: %s %s", r.Method, r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing auth")
				}
				b, _ := io.ReadAll(r.Body)
				if method == "POST" || method == "PUT" || method == "PATCH" {
					if string(b) != `{"name":"value"}` || r.Header.Get("Content-Type") != "application/json" {
						t.Errorf("bad body: %s", b)
					}
				} else if len(b) != 0 {
					t.Errorf("unexpected body %s", b)
				}
				w.Header().Set("X-Result", "ok")
				w.Write([]byte("done"))
			}))
			defer s.Close()
			var body any
			if method == "POST" || method == "PUT" || method == "PATCH" {
				body = map[string]string{"name": "value"}
			}
			headers := http.Header{"Authorization": {"Bearer secret"}}
			query := url.Values{"q": {"a & b"}, "tag": {"one", "two"}}
			got, err := client(t, s, Options{}).Do(context.Background(), method, "items/a%20b", query, body, headers)
			if err != nil || got.StatusCode != 200 || got.Headers.Get("X-Result") != "ok" {
				t.Fatalf("%+v %v", got, err)
			}
			if method != "HEAD" && string(got.Body) != "done" {
				t.Fatalf("body %s", got.Body)
			}
			if headers.Get("Content-Type") != "" || query.Get("q") != "a & b" {
				t.Fatal("caller input mutated")
			}
		})
	}
}

func TestBounds(t *testing.T) {
	for _, size := range []int{0, 3, 4, 5} {
		for _, stream := range []bool{false, true} {
			t.Run(strings.Repeat("x", size)+map[bool]string{true: "stream", false: "buffer"}[stream], func(t *testing.T) {
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.(http.Flusher).Flush()
					w.Write([]byte(strings.Repeat("x", size)))
				}))
				defer s.Close()
				c := client(t, s, Options{MaxBodyBytes: 4})
				var data []byte
				var err error
				if stream {
					var resp *http.Response
					resp, err = c.Open(context.Background(), "GET", "", nil, nil, nil)
					if err == nil {
						defer resp.Body.Close()
						data, err = io.ReadAll(resp.Body)
					}
				} else {
					var resp Response
					resp, err = c.Do(context.Background(), "GET", "", nil, nil, nil)
					data = resp.Body
				}
				if (size > 4) != errors.Is(err, ErrBodyTooLarge) || (size <= 4 && err != nil) || len(data) != min(size, 4) {
					t.Fatalf("len=%d err=%v", len(data), err)
				}
			})
		}
	}
}

func TestDeadlineAndClose(t *testing.T) {
	for _, mode := range []string{"headers", "stream", "close", "parent", "callback"} {
		t.Run(mode, func(t *testing.T) {
			canceled := make(chan struct{})
			started := make(chan struct{})
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(started)
				if mode != "headers" {
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
				close(canceled)
			}))
			defer s.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c := client(t, s, Options{Timeout: 100 * time.Millisecond})
			resp, err := c.Open(ctx, "GET", "", nil, nil, nil)
			if mode == "headers" {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.Request == nil {
					t.Fatal("missing stream request")
				}
				if _, ok := resp.Request.Context().Deadline(); !ok {
					t.Fatal("missing stream deadline")
				}
				if mode == "callback" {
					select {
					case <-resp.Request.Context().Done():
						if !errors.Is(resp.Request.Context().Err(), context.DeadlineExceeded) {
							t.Fatal("callback context did not time out")
						}
					case <-time.After(time.Second):
						t.Fatal("callback context lacks deadline cancellation")
					}
				}
				if mode == "close" {
					if err := resp.Body.Close(); err != nil {
						t.Fatal(err)
					}
					if !errors.Is(resp.Request.Context().Err(), context.Canceled) {
						t.Fatal("Close did not cancel request context")
					}
				} else {
					if mode == "parent" {
						cancel()
					}
					_, err = io.ReadAll(resp.Body)
					want := context.DeadlineExceeded
					if mode == "parent" {
						want = context.Canceled
					}
					if !errors.Is(err, want) {
						t.Fatalf("%v", err)
					}
				}
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("server did not observe cancellation")
			}
		})
	}
}

func TestErrorsAndRedirects(t *testing.T) {
	var follows atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { follows.Add(1) }))
	defer target.Close()
	for _, status := range []int{302, 307, 400, 500} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", target.URL)
			w.WriteHeader(status)
			w.Write([]byte("Bearer secret query-secret failure"))
		}))
		supplied := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { t.Error("supplied redirect hook called"); return nil }}
		c := client(t, s, Options{HTTP: supplied})
		resp, err := c.Open(context.Background(), "POST", "", url.Values{"token": {"query-secret"}}, nil, http.Header{"Authorization": {"Bearer secret"}})
		var he *HTTPError
		if resp != nil || !errors.As(err, &he) || he.StatusCode != status || !strings.Contains(string(he.Body), "failure") || strings.Contains(string(he.Body), "secret") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("resp=%v err=%v detail=%+v", resp, err, he)
		}
		if supplied.CheckRedirect == nil {
			t.Fatal("mutated supplied client")
		}
		s.Close()
	}
	if follows.Load() != 0 {
		t.Fatal("followed redirect")
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500); w.Write([]byte("too long")) }))
	defer s.Close()
	_, err := client(t, s, Options{MaxBodyBytes: 2}).Open(context.Background(), "GET", "", nil, nil, nil)
	var he *HTTPError
	if !errors.As(err, &he) || !errors.Is(err, ErrBodyTooLarge) || string(he.Body) != "to" {
		t.Fatalf("%v", err)
	}
}

func TestInvalidInputs(t *testing.T) {
	for _, base := range []string{"", "relative", "ftp://localhost", "http://", "http://u:p@localhost", "http://localhost/#", "http://localhost/#f", "http://localhost/?q=x", "http://localhost:0", "http://localhost:99999", "http://localhost:", "http://localhost/a/../b", "http://localhost/%252e%252e"} {
		if _, err := New(base, Options{}); err == nil {
			t.Errorf("accepted %q", base)
		}
	}
	for _, o := range []Options{{Timeout: -1}, {MaxBodyBytes: -1}} {
		if _, err := New("http://localhost", o); err == nil {
			t.Error("accepted negative option")
		}
	}
	c, err := New("http://localhost", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if c.timeout != DefaultTimeout || c.maxBody != DefaultMaxBodyBytes {
		t.Fatal("wrong defaults")
	}
	for _, path := range []string{"../x", "/../x", "a/./b", "//other/x", "https://other/x", "%2e%2e/x", "%252e%252e/x", "/%2fother/x", `a\b`, "a?x=y", "a#x", "%zz"} {
		if _, err := c.Open(context.Background(), "GET", path, nil, nil, nil); err == nil || !strings.Contains(err.Error(), "invalid request path") {
			t.Errorf("path %q: %v", path, err)
		}
	}
	for _, method := range []string{"GET", "DELETE", "HEAD", "INVALID"} {
		if _, err := c.Open(context.Background(), method, "", nil, "ignored", nil); err == nil {
			t.Errorf("accepted %s body", method)
		}
	}
	if _, err := c.Open(context.Background(), "POST", "", nil, make(chan int), nil); err == nil {
		t.Fatal("accepted non-JSON")
	}
	if _, err := c.Open(nil, "GET", "", nil, nil, nil); err == nil {
		t.Fatal("accepted nil context")
	}
}

func TestDisconnectDoesNotReplayWrites(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			var attempts atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					w.Write([]byte("warm"))
					return
				}
				attempts.Add(1)
				io.Copy(io.Discard, r.Body)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				conn.Close()
			}))
			defer s.Close()
			tr := http.DefaultTransport.(*http.Transport).Clone()
			defer tr.CloseIdleConnections()
			c := client(t, s, Options{HTTP: &http.Client{Transport: tr}})
			if _, err := c.Do(context.Background(), "GET", "", nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			var body any
			if method != "DELETE" {
				body = map[string]string{"action": "write"}
			}
			_, err := c.Do(context.Background(), method, "", nil, body, http.Header{"Idempotency-Key": {"key"}, "X-Idempotency-Key": {"key"}})
			if err == nil || attempts.Load() != 1 {
				t.Fatalf("attempts=%d err=%v", attempts.Load(), err)
			}
		})
	}
}

// Force transport EOF while complete JSON records remain buffered for callbacks.
func TestBufferedRecordsAfterEOF(t *testing.T) {
	const payload = `{"id":1}{"id":2}`
	for _, exactLimit := range []bool{false, true} {
		name := "below-limit"
		if exactLimit {
			name = "exact-limit-probe"
		}
		t.Run(name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.(http.Flusher).Flush()
				io.WriteString(w, payload)
			}))
			defer s.Close()
			limit := int64(len(payload) + 1)
			if exactLimit {
				limit--
			}
			for _, finish := range []string{"close", "deadline"} {
				t.Run(finish, func(t *testing.T) {
					timeout := 5 * time.Second
					if finish == "deadline" {
						timeout = 100 * time.Millisecond
					}
					resp, err := client(t, s, Options{MaxBodyBytes: limit, Timeout: timeout}).Open(context.Background(), "GET", "", nil, nil, nil)
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					buffered := bufio.NewReader(resp.Body)
					if _, err := buffered.Peek(len(payload) + 1); err != io.EOF {
						t.Fatalf("expected transport EOF, got %v", err)
					}
					decoder := json.NewDecoder(buffered)
					callbackCtx := resp.Request.Context()
					for want := 1; want <= 2; want++ {
						var record struct {
							ID int `json:"id"`
						}
						if err := decoder.Decode(&record); err != nil {
							t.Fatal(err)
						}
						if err := callbackCtx.Err(); err != nil {
							t.Fatalf("buffered record callback %d canceled: %v", want, err)
						}
						if record.ID != want {
							t.Fatalf("record ID = %d, want %d", record.ID, want)
						}
					}
					var extra any
					if err := decoder.Decode(&extra); err != io.EOF {
						t.Fatalf("expected decoder EOF, got %v", err)
					}
					if finish == "close" {
						if err := resp.Body.Close(); err != nil {
							t.Fatal(err)
						}
						if !errors.Is(callbackCtx.Err(), context.Canceled) {
							t.Fatalf("Close did not cancel context: %v", callbackCtx.Err())
						}
					} else {
						select {
						case <-callbackCtx.Done():
							if !errors.Is(callbackCtx.Err(), context.DeadlineExceeded) {
								t.Fatalf("deadline lost after EOF: %v", callbackCtx.Err())
							}
						case <-time.After(time.Second):
							t.Fatal("timer stopped at EOF")
						}
					}
				})
			}
		})
	}
}

func TestHTTPErrorPreservesMetadataAndRedactsCredentials(t *testing.T) {
	headers := http.Header{
		"Authorization":       {"Bearer auth-secret"},
		"Proxy-Authorization": {"Basic proxy-secret"},
		"Cookie":              {"session=cookie-secret; other=second-cookie-secret"},
		"X-Api-Key":           {"header-api-secret"},
		"X-Request-Id":        {"request-123"},
		"X-Metadata":          {"harmless metadata"},
	}
	query := url.Values{
		"id":           {"item-456"},
		"token":        {"query-token-secret"},
		"access_token": {"access-secret"},
		"api_key":      {"query-api-secret"},
	}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		// Echo whole credential values and bare tokens as real error services can.
		for _, values := range r.Header {
			for _, value := range values {
				io.WriteString(w, value+"\n")
			}
		}
		for _, values := range r.URL.Query() {
			for _, value := range values {
				io.WriteString(w, value+"\n")
			}
		}
		io.WriteString(w, "auth-secret proxy-secret cookie-secret second-cookie-secret\nBearer Basic\n")
	}))
	defer s.Close()
	_, err := client(t, s, Options{}).Open(context.Background(), "GET", "", query, nil, headers)
	var he *HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("expected HTTPError, got %v", err)
	}
	body := string(he.Body)
	for _, value := range []string{"item-456", "request-123", "harmless metadata", "Bearer Basic"} {
		if !strings.Contains(body, value) {
			t.Errorf("innocuous metadata %q lost: %s", value, body)
		}
	}
	for _, secret := range []string{"auth-secret", "proxy-secret", "cookie-secret", "second-cookie-secret", "header-api-secret", "query-token-secret", "access-secret", "query-api-secret"} {
		if strings.Contains(body, secret) {
			t.Errorf("credential %q exposed", secret)
		}
	}
	if err.Error() != "rpc: HTTP status 400" {
		t.Fatalf("unexpected diagnostic: %v", err)
	}
}
