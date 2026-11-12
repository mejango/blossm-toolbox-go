package transport

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBoundaryErrors(t *testing.T) {
	for _, body := range []string{`{"device":{},"unknown":true}`, `{} {}`, `{"device":{},"device":{}}`, strings.Repeat("x", (1<<20)+1)} {
		req := httptest.NewRequest("POST", "/", strings.NewReader(body))
		w := httptest.NewRecorder()
		h := Wrap(func(w http.ResponseWriter, r *http.Request) error {
			var in struct {
				Device struct{} `json:"device"`
			}
			return Decode(w, r, &in)
		})
		h(w, req)
		if w.Code != 400 {
			t.Fatalf("status%d", w.Code)
		}
	}
}

func TestSerializationFailureCannotCommitSuccessStatus(t *testing.T) {
	response := httptest.NewRecorder()
	Wrap(func(w http.ResponseWriter, r *http.Request) error { return Write(w, 200, make(chan int)) })(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 500 {
		t.Fatalf("serialization failure returned %d", response.Code)
	}
}
