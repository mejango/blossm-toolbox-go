package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

// CommandClient calls an explicitly addressed service without retries. Target is
// its stable logical identity, not a deployment URL. Authorize must supply valid
// target credentials carrying the intended delegated identity. The receiver's
// Framework.Authorize reconstructs trusted context/claims from those credentials.
// A child commit cannot be rolled back by a later parent failure.
type CommandClient struct {
	RPC       *rpc.Client
	Target    string
	Authorize func(context.Context, command.Request) (http.Header, error)
}

// Execute derives a stable child ID from the parent ID, target, command name and
// explicit operation selector (e.g. "reserve/item-42"). Reusing a selector means
// retrying that same operation; use distinct selectors for independent effects.
// Child ID/Issued/Transaction/Context/Claims are envelope-owned, not taken from in.
func (c CommandClient) Execute(ctx context.Context, parent command.Request, name, operation string, in command.Request) (command.Response, error) {
	var out command.Response
	if c.RPC == nil || c.Authorize == nil {
		return out, errors.New("command RPC client and target authenticator required")
	}
	if strings.TrimSpace(c.Target) == "" || len(c.Target) > 1024 || strings.TrimSpace(parent.ID) == "" || len(parent.ID) > 1024 || strings.TrimSpace(operation) == "" || len(operation) > 1024 || !commandName(name) {
		return out, command.ErrInvalid
	}
	id, err := eventstore.Digest([]string{"blossm.command.child.v1", parent.ID, c.Target, name, operation})
	if err != nil {
		return out, err
	}
	in.ID = id
	in.Issued = parent.Issued
	in.Transaction = eventstore.CloneEvent(eventstore.Event{Input: eventstore.Input{Transaction: parent.Transaction}}).Input.Transaction
	if in.Transaction.ID == "" {
		in.Transaction.ID = parent.ID
	}
	if in.Transaction.Issued == "" {
		in.Transaction.Issued = parent.Issued
	}
	// Body authority must never become a fallback if delegated authentication fails.
	in.Context = nil
	in.Claims = nil
	in.Transaction.Claims = nil
	headers, err := c.Authorize(ctx, parent)
	if err != nil {
		return out, err
	}
	response, err := c.RPC.Do(ctx, http.MethodPost, "/commands/"+name, nil, in, headers)
	if err != nil {
		return out, err
	}
	if response.StatusCode == http.StatusNoContent {
		out = command.Response{ID: id, TransactionID: in.Transaction.ID, Receipts: []command.Receipt{}, Contribution: command.Contribution{StatusCode: http.StatusNoContent}}
	} else {
		if err = eventstore.Decode(response.Body, &out); err != nil {
			return out, fmt.Errorf("invalid command response: %w", err)
		}
		if out.ID != id || out.TransactionID != in.Transaction.ID {
			return command.Response{}, errors.New("command response identity mismatch")
		}
	}
	if response.Headers.Get("Blossm-Callback-Status") == "failed" {
		return out, &command.CallbackError{Err: errors.New("remote response callback failed")}
	}
	return out, nil
}

func commandName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 256 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
