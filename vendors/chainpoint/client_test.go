package chainpoint

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestSubmitAndRetrieve(t *testing.T) {
	hash := strings.Repeat("ab", 32)
	id := "d71e0960-5b66-11ea-bd26-817245cf7a26"
	missing := false
	c, err := New("https://gateway.example", &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Path {
		case "/hashes":
			var v struct {
				Hashes []string `json:"hashes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&v); err != nil || len(v.Hashes) != 1 || v.Hashes[0] != hash || r.Method != "POST" {
				t.Error("bad submission")
			}
			body = `{"hashes":[{"hash":"` + hash + `","proof_id":"` + id + `"}]}`
		case "/proofs":
			if r.Header.Get("proofIds") != id || r.Method != "GET" {
				t.Error("bad proof request")
			}
			body = `[{"proof_id":"` + id + `","proof":"encoded-proof","anchors_complete":["cal"]}]`
			if missing {
				body = "[]"
			}
		default:
			t.Fatal(r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	handles, err := c.SubmitHash(ctx, hash)
	if err != nil || len(handles) != 1 {
		t.Fatal(handles, err)
	}
	proof, err := c.GetProof(ctx, handles[0])
	if err != nil || string(proof.Value) != `"encoded-proof"` || proof.AnchorsComplete[0] != "cal" {
		t.Fatal(proof, err)
	}
	missing = true
	if p, err := c.GetProof(ctx, handles[0]); err != nil || p != nil {
		t.Fatal(p, err)
	}
	if _, err = c.GetProof(ctx, Handle{id, "https://untrusted.example"}); err == nil {
		t.Fatal("accepted foreign proof endpoint")
	}
}
