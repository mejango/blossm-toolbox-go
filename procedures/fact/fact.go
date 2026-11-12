// Package fact owns validated read procedures and serial, cancellation-aware streams.
package fact

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"unicode/utf8"

	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

var ErrInvalid = errors.New("invalid fact input")

var ErrInvalidRecord = errors.New("fact stream requires a complete UTF-8 JSON object or array")
var ErrStreamClosed = errors.New("fact stream already completed")

type Request struct {
	Query   json.RawMessage
	Root    string
	Context command.Context
	Claims  json.RawMessage
}
type Yield func(context.Context, json.RawMessage) error

type Procedure struct {
	RequiredContexts []string
	Validate         func(context.Context, Request) error
	ReadFn           func(context.Context, Request) (json.RawMessage, error)
	StreamFn         func(context.Context, Request, Yield) error
}

func (p Procedure) prepare(ctx context.Context, in Request) (Request, error) {
	if err := ctx.Err(); err != nil {
		return in, err
	}
	if err := command.RequireContext(in.Context, p.RequiredContexts); err != nil {
		return in, err
	}
	if len(in.Query) == 0 {
		in.Query = json.RawMessage(`{}`)
	}
	query, err := eventstore.Canonical(in.Query)
	if err != nil {
		return in, errors.Join(ErrInvalid, err)
	}
	in.Query = query
	if p.Validate != nil {
		if err = p.Validate(ctx, in); err != nil {
			return in, errors.Join(ErrInvalid, err)
		}
	}
	return in, err
}
func (p Procedure) Read(ctx context.Context, in Request) (json.RawMessage, error) {
	in, err := p.prepare(ctx, in)
	if err != nil {
		return nil, err
	}
	if p.ReadFn == nil {
		return nil, errors.New("fact read procedure not configured")
	}
	raw, err := p.ReadFn(ctx, in)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return eventstore.Canonical(raw)
}

// Stream awaits every callback, serializing even an accidentally concurrent
// producer. Producers must stop on yield errors and honor ctx during their work.
// A callback must also honor ctx: arbitrary caller code cannot be forcibly stopped.
func (p Procedure) Stream(ctx context.Context, in Request, yield Yield) error {
	in, err := p.prepare(ctx, in)
	if err != nil {
		return err
	}
	if p.StreamFn == nil || yield == nil {
		return errors.New("fact stream producer and callback required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mutex sync.Mutex
	closed := false
	var first error
	emit := func(_ context.Context, raw json.RawMessage) error {
		mutex.Lock()
		defer mutex.Unlock()
		if closed {
			return ErrStreamClosed
		}
		if first != nil {
			return first
		}
		first = ctx.Err()
		if first == nil {
			first = validRecord(raw)
		}
		if first == nil {
			first = yield(ctx, append(json.RawMessage(nil), raw...))
		}
		if first != nil {
			cancel()
		}
		return first
	}
	sourceErr := p.StreamFn(ctx, in, emit)
	mutex.Lock()
	closed = true
	callbackErr := first
	mutex.Unlock()
	return errors.Join(sourceErr, callbackErr, ctx.Err())
}

func validRecord(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || (raw[0] != '{' && raw[0] != '[') || !utf8.Valid(raw) || !json.Valid(raw) {
		return ErrInvalidRecord
	}
	return nil
}

// Client uses explicit RPC addressing; authentication headers are consumer-owned.
type Client struct{ RPC *rpc.Client }

func (c Client) Read(ctx context.Context, path string, query url.Values, headers http.Header) (json.RawMessage, error) {
	if c.RPC == nil {
		return nil, errors.New("fact RPC client required")
	}
	response, err := c.RPC.Do(ctx, http.MethodGet, path, query, nil, headers)
	if err != nil {
		return nil, err
	}
	return eventstore.Canonical(response.Body)
}

// Stream decodes concatenated JSON objects/arrays independent of transport
// chunks. Each callback completes before the next record is decoded. The RPC
// client owns the total body/deadline bound, and response closure cancels it.
func (c Client) Stream(ctx context.Context, path string, query url.Values, headers http.Header, yield Yield) error {
	if c.RPC == nil || yield == nil {
		return errors.New("fact RPC client and callback required")
	}
	response, err := c.RPC.Open(ctx, http.MethodGet, path, query, nil, headers)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	ctx = response.Request.Context()
	decoder := json.NewDecoder(response.Body)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		var raw json.RawMessage
		err = decoder.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if err = validRecord(raw); err != nil {
			return err
		}
		if err = yield(ctx, raw); err != nil {
			return err
		}
	}
}

// StreamRaw supports explicit binary/text consumers without JSON framing.
func (c Client) StreamRaw(ctx context.Context, path string, query url.Values, headers http.Header, yield func(context.Context, []byte) error) error {
	if c.RPC == nil || yield == nil {
		return errors.New("fact RPC client and callback required")
	}
	response, err := c.RPC.Open(ctx, http.MethodGet, path, query, nil, headers)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	ctx = response.Request.Context()
	buffer := make([]byte, 32<<10)
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if err = yield(ctx, append([]byte(nil), buffer[:n]...)); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
