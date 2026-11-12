package fact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/rpc"
)

func client(t *testing.T, handler http.HandlerFunc, options rpc.Options) Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	transport, err := rpc.New(server.URL, options)
	if err != nil {
		t.Fatal(err)
	}
	return Client{RPC: transport}
}
func TestReadAndChunkIndependentStream(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" || r.URL.Query().Get("query") != `{"limit":2}` {
			http.Error(w, "invalid", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/read" {
			_, _ = w.Write([]byte(`{"answer":42}`))
			return
		}
		for _, b := range []byte("{\"name\":\"é😀\"}\n[1,{\"nested\":true}]") {
			_, _ = w.Write([]byte{b})
			w.(http.Flusher).Flush()
		}
	}, rpc.Options{})
	query := url.Values{"query": []string{`{"limit":2}`}}
	headers := http.Header{"Authorization": []string{"Bearer test"}}
	raw, err := c.Read(context.Background(), "/read", query, headers)
	if err != nil || string(raw) != `{"answer":42}` {
		t.Fatal(string(raw), err)
	}
	var values []string
	err = c.Stream(context.Background(), "/stream", query, headers, func(_ context.Context, raw json.RawMessage) error { values = append(values, string(raw)); return nil })
	if err != nil || !reflect.DeepEqual(values, []string{`{"name":"é😀"}`, `[1,{"nested":true}]`}) {
		t.Fatal(values, err)
	}
	if query.Get("query") != `{"limit":2}` {
		t.Fatal("query mutated")
	}
}
func TestStreamAwaitsSerialCallbacks(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, `{"i":1}{"i":2}`) }, rpc.Options{})
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	var calls atomic.Int32
	go func() {
		done <- c.Stream(context.Background(), "/", nil, nil, func(context.Context, json.RawMessage) error {
			if calls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return nil
		})
	}()
	<-entered
	select {
	case <-done:
		t.Fatal("stream completed before callback")
	default:
	}
	if calls.Load() != 1 {
		t.Fatal("callbacks overlapped")
	}
	close(release)
	if err := <-done; err != nil || calls.Load() != 2 {
		t.Fatal(err, calls.Load())
	}
}
func TestCallbackErrorClosesNetworkStream(t *testing.T) {
	disconnected := make(chan struct{})
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"first":true}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}, rpc.Options{})
	cause := errors.New("consumer stopped")
	err := c.Stream(context.Background(), "/", nil, nil, func(context.Context, json.RawMessage) error { return cause })
	if !errors.Is(err, cause) {
		t.Fatal(err)
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("response was not closed")
	}
}
func TestStreamingDeadlineReachesCallbackAndCancelsServer(t *testing.T) {
	disconnected := make(chan struct{})
	c := client(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"first":true}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(disconnected)
	}, rpc.Options{Timeout: 50 * time.Millisecond})
	started := time.Now()
	err := c.Stream(context.Background(), "/", nil, nil, func(ctx context.Context, _ json.RawMessage) error { <-ctx.Done(); return ctx.Err() })
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatal("callback missed RPC timeout", err)
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("deadline did not close network")
	}
}
func TestMalformedOversizedAndFailedStreams(t *testing.T) {
	for name, body := range map[string]string{"truncated": `{"unfinished":`, "invalid-utf8": string([]byte{'{', '"', 'x', '"', ':', '"', 255, '"', '}'}), "scalar": `42`, "trailing-garbage": `{}oops`} {
		t.Run(name, func(t *testing.T) {
			c := client(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, body) }, rpc.Options{})
			if err := c.Stream(context.Background(), "/", nil, nil, func(context.Context, json.RawMessage) error { return nil }); err == nil {
				t.Fatal("bad stream accepted")
			}
		})
	}
	t.Run("oversized", func(t *testing.T) {
		c := client(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"data":"%s"}`, strings.Repeat("x", 100))
		}, rpc.Options{MaxBodyBytes: 32})
		if err := c.Stream(context.Background(), "/", nil, nil, func(context.Context, json.RawMessage) error { return nil }); !errors.Is(err, rpc.ErrBodyTooLarge) {
			t.Fatal(err)
		}
	})
	t.Run("http-error", func(t *testing.T) {
		c := client(t, func(w http.ResponseWriter, r *http.Request) { http.Error(w, "unavailable", 503) }, rpc.Options{})
		called := false
		err := c.Stream(context.Background(), "/", nil, nil, func(context.Context, json.RawMessage) error { called = true; return nil })
		var httpErr *rpc.HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != 503 || called {
			t.Fatal(err, called)
		}
	})
}
func TestProcedureRequiredContextAndSerialCleanup(t *testing.T) {
	cleaned := false
	called := false
	p := Procedure{RequiredContexts: []string{"principal"}, StreamFn: func(ctx context.Context, in Request, yield Yield) error {
		called = true
		defer func() { cleaned = true }()
		for i := 0; i < 3; i++ {
			if err := yield(ctx, json.RawMessage(`{}`)); err != nil {
				return err
			}
		}
		return nil
	}}
	if err := p.Stream(context.Background(), Request{}, func(context.Context, json.RawMessage) error { return nil }); !errors.Is(err, command.ErrForbidden) || called {
		t.Fatal(err, called)
	}
	cause := errors.New("stop")
	calls := 0
	err := p.Stream(context.Background(), Request{Context: command.Context{"principal": json.RawMessage(`"one"`)}}, func(context.Context, json.RawMessage) error { calls++; return cause })
	if !errors.Is(err, cause) || calls != 1 || !cleaned {
		t.Fatal(err, calls, cleaned)
	}
}
func TestConcurrentProducerSerializedAndClosed(t *testing.T) {
	var late Yield
	p := Procedure{StreamFn: func(ctx context.Context, _ Request, yield Yield) error {
		late = yield
		var wg sync.WaitGroup
		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func() { defer wg.Done(); _ = yield(ctx, json.RawMessage(`{}`)) }()
		}
		wg.Wait()
		return nil
	}}
	var active, calls atomic.Int32
	err := p.Stream(context.Background(), Request{}, func(context.Context, json.RawMessage) error {
		if active.Add(1) != 1 {
			t.Error("overlapping callbacks")
		}
		calls.Add(1)
		active.Add(-1)
		return nil
	})
	if err != nil || calls.Load() != 5 {
		t.Fatal(err, calls.Load())
	}
	if err = late(context.Background(), json.RawMessage(`{}`)); !errors.Is(err, ErrStreamClosed) {
		t.Fatal("late producer accepted", err)
	}
}
func TestRawStream(t *testing.T) {
	c := client(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte{0, 255, 42}) }, rpc.Options{})
	var got []byte
	if err := c.StreamRaw(context.Background(), "/", nil, nil, func(_ context.Context, b []byte) error { got = append(got, b...); return nil }); err != nil || !reflect.DeepEqual(got, []byte{0, 255, 42}) {
		t.Fatal(got, err)
	}
}

func TestInvalidQueryClassification(t *testing.T) {
	validation := errors.New("invalid filter")
	readFailure := errors.New("database unavailable")
	for _, test := range []struct {
		query    string
		validate error
	}{
		{"{", nil}, {"{}", validation},
	} {
		p := Procedure{Validate: func(context.Context, Request) error { return test.validate },
			ReadFn: func(context.Context, Request) (json.RawMessage, error) {
				t.Fatal("invalid query reached reader")
				return nil, nil
			},
			StreamFn: func(context.Context, Request, Yield) error { t.Fatal("invalid query reached stream"); return nil }}
		_, err := p.Read(context.Background(), Request{Query: json.RawMessage(test.query)})
		if !errors.Is(err, ErrInvalid) || (test.validate != nil && !errors.Is(err, test.validate)) {
			t.Fatal(err)
		}
		if err = p.Stream(context.Background(), Request{Query: json.RawMessage(test.query)}, func(context.Context, json.RawMessage) error { return nil }); !errors.Is(err, ErrInvalid) {
			t.Fatal(err)
		}
	}
	p := Procedure{ReadFn: func(context.Context, Request) (json.RawMessage, error) { return nil, readFailure }}
	_, err := p.Read(context.Background(), Request{})
	if !errors.Is(err, readFailure) || errors.Is(err, ErrInvalid) {
		t.Fatal("backend failure reclassified", err)
	}
}
