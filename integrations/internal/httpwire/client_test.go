package httpwire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedirectAndBoundedErrors(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed") }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, target.URL, 302)
		case "/oversize":
			io.WriteString(w, strings.Repeat("x", ResponseLimit+1))
		default:
			w.WriteHeader(403)
			io.WriteString(w, "secret")
		}
	}))
	defer origin.Close()
	c, err := New(origin.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/redirect", "/oversize", "/denied"} {
		_, _, err = c.Do(context.Background(), "GET", path, nil, nil)
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = c.Do(ctx, "GET", "/denied", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com?token=secret"} {
		if _, err := New(endpoint, nil); err == nil {
			t.Fatal("accepted unsafe endpoint")
		}
	}
}
