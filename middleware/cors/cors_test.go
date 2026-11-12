package cors

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORS(t *testing.T) {
	calls := 0
	h, e := CORS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(204) }), []string{"https://app.test"})
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct {
		origin string
		status int
	}{{"https://app.test", 204}, {"https://evil.test", 403}} {
		r := httptest.NewRequest("OPTIONS", "/", nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Access-Control-Request-Method", "POST")
		r.Header.Set("Access-Control-Request-Headers", "Authorization,Idempotency-Key")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal(w.Code)
		}
	}
	if calls != 0 {
		t.Fatal("preflight invoked command")
	}
	if _, e = CORS(h, []string{"*"}); e == nil {
		t.Fatal("wildcard accepted")
	}
}
