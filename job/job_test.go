package job

import (
	"blossm.local/backend/rpc"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestJobHTTP(t *testing.T) {
	calls := 0
	p := Procedure{Run: func(ctx context.Context, in Input) error {
		calls++
		if string(in.Claims) != `{"sub":"trusted"}` || string(in.Context) != `{"principal":"trusted"}` {
			t.Fatal("body identity trusted", in)
		}
		if string(in.Payload) == `{"fail":true}` {
			return errors.New("fixture")
		}
		return ctx.Err()
	}}
	h, e := p.Handler(func(r *http.Request) (Identity, error) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			return Identity{}, errors.New("auth")
		}
		return Identity{Context: json.RawMessage(`{"principal":"trusted"}`), Claims: json.RawMessage(`{"sub":"trusted"}`)}, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(h)
	defer server.Close()
	client, e := rpc.New(server.URL, rpc.Options{})
	if e != nil {
		t.Fatal(e)
	}
	c := Client{client}
	in := Input{Payload: json.RawMessage(`{}`), Context: json.RawMessage(`{"principal":"forged"}`), Claims: json.RawMessage(`{"sub":"forged"}`)}
	if e = c.Execute(context.Background(), "/job", in, nil); e == nil {
		t.Fatal("missing auth accepted")
	}
	headers := http.Header{"Authorization": []string{"Bearer fixture"}}
	if e = c.Execute(context.Background(), "/job", in, headers); e != nil {
		t.Fatal(e)
	}
	in.Payload = json.RawMessage(`{"fail":true}`)
	if e = c.Execute(context.Background(), "/job", in, headers); e == nil {
		t.Fatal("hidden failure")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
