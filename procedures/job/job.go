// Package job runs explicitly authenticated scheduled HTTP jobs.
package job

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"io"
	"net/http"
	"time"
)

type Input struct {
	Payload json.RawMessage `json:"payload"`
	Context json.RawMessage `json:"context,omitempty"`
	Claims  json.RawMessage `json:"claims,omitempty"`
}

// Identity is supplied only by the trusted authenticator.
type Identity struct{ Context, Claims json.RawMessage }

type Procedure struct {
	Run func(context.Context, Input) error
}

func (p Procedure) Execute(ctx context.Context, in Input) error {
	if p.Run == nil {
		return errors.New("job implementation required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	normalized, e := eventstore.Canonical(in.Payload)
	if e != nil {
		return e
	}
	in.Payload = normalized
	return p.Run(ctx, in)
}

// Authorize supplies service authentication. It must verify the actual configured
// scheduler/service identity, not trust request-body claims.
func (p Procedure) Handler(authorize func(*http.Request) (Identity, error)) (http.Handler, error) {
	if authorize == nil || p.Run == nil {
		return nil, errors.New("job and service authenticator required")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		identity, e := authorize(r)
		if e != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		var in Input
		if e == nil {
			e = eventstore.Decode(body, &in)
		}
		if e != nil {
			http.Error(w, "invalid job input", 400)
			return
		}
		in.Context = append(json.RawMessage(nil), identity.Context...)
		in.Claims = append(json.RawMessage(nil), identity.Claims...)
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if e = p.Execute(ctx, in); e != nil {
			http.Error(w, "job failed", 500)
			return
		}
		w.WriteHeader(204)
	}), nil
}

// Client is a thin named operation over the shared cancellation-aware RPC client.
type Client struct{ RPC *rpc.Client }

func (c Client) Execute(ctx context.Context, path string, in Input, headers http.Header) error {
	if c.RPC == nil {
		return errors.New("job RPC client required")
	}
	response, err := c.RPC.Do(ctx, http.MethodPost, path, nil, in, headers)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return errors.New("unexpected job acknowledgment")
	}
	return nil
}
