package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/mejango/blossm-toolbox-go/procedures/projection"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/security/authn"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"github.com/mejango/blossm-toolbox-go/stores/view"
)

// ProjectionOperation contains untrusted request selectors, not granted authority.
// Resolve must authenticate the request and authorize this operation/root before
// returning a binding. Replay authority covers the entire returned source.
type ProjectionOperation struct {
	Name   string
	RootID string
	Replay bool
}

// ProjectionBinding is supplied by trusted composition, never decoded from JSON.
// Engine's dependency handlers and scoped Views writer must be configured by the
// application. A permitted source is not permission to use an unscoped writer.
type ProjectionBinding struct {
	Source    projection.Source
	Engine    *projection.Engine
	Scheduler *projection.Scheduler
}
type ProjectionRoutes struct {
	Resolve func(*http.Request, ProjectionOperation) (ProjectionBinding, error)
}

type ProjectionPlayInput struct {
	RootID string `json:"rootId"`
	Flag   string `json:"flag,omitempty"`
}
type ProjectionReplayInput struct {
	RunID projection.RunID `json:"runId"`
	After string           `json:"after,omitempty"`
	Limit int              `json:"limit"`
}

// Wire responses preserve durable outcomes while omitting private error strings.
type ProjectionPlayOutput struct {
	Rows         []view.Result             `json:"rows"`
	Visited      int                       `json:"visited"`
	Ignored      bool                      `json:"ignored"`
	Committed    bool                      `json:"committed"`
	Uncertain    bool                      `json:"uncertain"`
	PushFailures []projection.Notification `json:"pushFailures,omitempty"`
	Error        string                    `json:"error,omitempty"`
}
type ProjectionReplayOutput struct {
	RunID    projection.RunID `json:"runId"`
	Enqueued int              `json:"enqueued"`
	After    string           `json:"after"`
	Complete bool             `json:"complete"`
	Error    string           `json:"error,omitempty"`
}

func (p ProjectionRoutes) Handler() (http.Handler, error) {
	if p.Resolve == nil {
		return nil, errors.New("projection authenticator/resolver required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /projections/{name}/play", Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in ProjectionPlayInput
		if err := Decode(w, r, &in); err != nil {
			return err
		}
		if strings.TrimSpace(in.RootID) == "" {
			return authn.ErrInvalid
		}
		binding, err := p.Resolve(r, ProjectionOperation{Name: r.PathValue("name"), RootID: in.RootID})
		if err != nil {
			return err
		}
		if err = projectionSourceValid(binding.Source); err != nil {
			return err
		}
		if binding.Engine == nil {
			return errors.New("projection engine required")
		}
		// No event action/revision is accepted from a manual invocation.
		result, err := binding.Engine.Play(r.Context(), projection.Request{Root: eventstore.Root{Network: binding.Source.Network, Service: binding.Source.Service, Domain: binding.Source.Domain, ID: in.RootID}, Flag: in.Flag})
		out := ProjectionPlayOutput{Rows: result.Rows, Visited: result.Visited, Ignored: result.Ignored, Committed: result.Committed, Uncertain: result.Uncertain}
		for _, failure := range result.PushFailures {
			out.PushFailures = append(out.PushFailures, failure.Notification)
		}
		status := http.StatusOK
		if err != nil {
			status, out.Error = projectionStatus(err)
		}
		return Write(w, status, out)
	}))
	mux.HandleFunc("POST /projections/{name}/replay", Wrap(func(w http.ResponseWriter, r *http.Request) error {
		var in ProjectionReplayInput
		if err := Decode(w, r, &in); err != nil {
			return err
		}
		if in.RunID.Validate() != nil || in.Limit < 1 || in.Limit > projection.MaxReplayBatch {
			return authn.ErrInvalid
		}
		binding, err := p.Resolve(r, ProjectionOperation{Name: r.PathValue("name"), Replay: true})
		if err != nil {
			return err
		}
		if err = projectionSourceValid(binding.Source); err != nil {
			return err
		}
		if binding.Scheduler == nil {
			return errors.New("projection scheduler required")
		}
		// Reject foreign-scope cursors before calling the scheduling owner.
		if in.After != "" {
			var root eventstore.Root
			if eventstore.Decode([]byte(in.After), &root) != nil || root.Network != binding.Source.Network || root.Service != binding.Source.Service || root.Domain != binding.Source.Domain || strings.TrimSpace(root.ID) == "" {
				return authn.ErrInvalid
			}
		}
		result, err := binding.Scheduler.Replay(r.Context(), binding.Source, in.RunID, in.After, in.Limit)
		out := ProjectionReplayOutput{RunID: result.RunID, Enqueued: result.Enqueued, After: result.After, Complete: result.Complete}
		status := http.StatusOK
		if err != nil {
			status, out.Error = projectionStatus(err)
		}
		return Write(w, status, out)
	}))
	return mux, nil
}
func projectionSourceValid(source projection.Source) error {
	if strings.TrimSpace(source.Network) == "" || strings.TrimSpace(source.Service) == "" || strings.TrimSpace(source.Domain) == "" {
		return errors.New("trusted projection source required")
	}
	return nil
}
func projectionStatus(err error) (int, string) {
	switch {
	case errors.Is(err, eventstore.ErrUncertain):
		return http.StatusServiceUnavailable, "commit uncertain; reconcile before retry"
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout, "deadline exceeded"
	case errors.Is(err, eventstore.ErrConflict), errors.Is(err, eventstore.ErrIdempotency):
		return http.StatusConflict, "conflict"
	case errors.Is(err, eventstore.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, eventstore.ErrInvalid):
		return http.StatusBadRequest, "invalid input"
	default:
		return http.StatusInternalServerError, "projection operation failed"
	}
}

// ProjectionClient wraps the shared bounded RPC transport. HTTP errors retain
// typed partial progress. Callers must inspect it and the error together. No
// request, enqueue or uncertain commit is retried by this adapter.
type ProjectionClient struct{ RPC *rpc.Client }

func (c ProjectionClient) Play(ctx context.Context, name string, in ProjectionPlayInput, headers http.Header) (ProjectionPlayOutput, error) {
	var out ProjectionPlayOutput
	err := c.call(ctx, name, "play", in, headers, &out)
	if err == nil && out.Error != "" {
		err = errors.New("projection returned an operation error")
	}
	return out, err
}
func (c ProjectionClient) Replay(ctx context.Context, name string, in ProjectionReplayInput, headers http.Header) (ProjectionReplayOutput, error) {
	var out ProjectionReplayOutput
	err := c.call(ctx, name, "replay", in, headers, &out)
	if err == nil && out.Error != "" {
		err = errors.New("projection returned an operation error")
	}
	return out, err
}
func (c ProjectionClient) call(ctx context.Context, name, operation string, in any, headers http.Header, out any) error {
	if c.RPC == nil {
		return errors.New("projection RPC client required")
	}
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, "/\\%\x00\r\n") || name == "." || name == ".." {
		return errors.New("invalid projection name")
	}
	response, err := c.RPC.Do(ctx, http.MethodPost, "/projections/"+url.PathEscape(name)+"/"+operation, nil, in, headers)
	if err != nil {
		var httpErr *rpc.HTTPError
		if errors.As(err, &httpErr) {
			// Preserve the original transport error even when the server sent a generic
			// pre-execution authorization/validation error rather than a result envelope.
			if e := decodeProjectionAck(httpErr.Body, in, out, false); e != nil {
				return fmt.Errorf("invalid projection error response: %w", err)
			}
		}
		return err
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("unexpected projection acknowledgment")
	}
	if err = decodeProjectionAck(response.Body, in, out, true); err != nil {
		return fmt.Errorf("invalid projection response: %w", err)
	}
	return nil
}

// decodeProjectionAck validates presence separately from zero values. Decode
// into a candidate so an invalid acknowledgment cannot publish trusted progress.
func decodeProjectionAck(data []byte, input, output any, success bool) error {
	var fields map[string]json.RawMessage
	if err := eventstore.Decode(data, &fields); err != nil || fields == nil {
		return errors.New("projection acknowledgment must be an object")
	}
	required := func(names ...string) error {
		for _, name := range names {
			if raw, ok := fields[name]; !ok || strings.TrimSpace(string(raw)) == "null" {
				return fmt.Errorf("projection acknowledgment requires %s", name)
			}
		}
		return nil
	}
	// Pre-execution authentication/validation errors have no progress envelope.
	if !success && len(fields) == 1 && fields["error"] != nil {
		var e struct {
			Error string `json:"error"`
		}
		if err := eventstore.Decode(data, &e); err != nil || e.Error == "" {
			return errors.New("invalid projection error")
		}
		return nil
	}
	switch target := output.(type) {
	case *ProjectionPlayOutput:
		if err := required("visited", "ignored", "committed", "uncertain"); err != nil {
			return err
		}
		if _, ok := fields["rows"]; !ok {
			return errors.New("projection acknowledgment requires rows")
		}
		if success {
			if err := required("rows"); err != nil {
				return err
			}
		}
		var candidate ProjectionPlayOutput
		if err := eventstore.Decode(data, &candidate); err != nil {
			return err
		}
		if candidate.Visited < 0 || (candidate.Committed && candidate.Uncertain) ||
			(candidate.Ignored && (candidate.Visited != 0 || candidate.Committed || candidate.Uncertain || len(candidate.Rows) != 0 || len(candidate.PushFailures) != 0)) ||
			(!candidate.Committed && (len(candidate.Rows) != 0 || len(candidate.PushFailures) != 0)) ||
			((candidate.Committed || candidate.Uncertain) && candidate.Visited == 0) ||
			(success && (candidate.Error != "" || candidate.Uncertain || (candidate.Committed && len(candidate.Rows) == 0) || (!candidate.Ignored && candidate.Visited == 0))) ||
			(!success && (candidate.Error == "" || candidate.Ignored)) {
			return errors.New("inconsistent projection play outcome")
		}
		if len(candidate.Rows) > 0 {
			var rows []map[string]json.RawMessage
			if err := eventstore.Decode(fields["rows"], &rows); err != nil {
				return err
			}
			for _, row := range rows {
				for _, name := range []string{"id", "applied", "version"} {
					raw, ok := row[name]
					if !ok || strings.TrimSpace(string(raw)) == "null" {
						return errors.New("incomplete projection row outcome")
					}
				}
			}
		}
		for _, row := range candidate.Rows {
			if strings.TrimSpace(row.ID) == "" || row.Version < -1 || (row.Applied && row.Version < 0) {
				return errors.New("invalid projection row outcome")
			}
		}
		*target = candidate
	case *ProjectionReplayOutput:
		if err := required("runId", "enqueued", "after", "complete"); err != nil {
			return err
		}
		var candidate ProjectionReplayOutput
		if err := eventstore.Decode(data, &candidate); err != nil {
			return err
		}
		request := input.(ProjectionReplayInput)
		if candidate.RunID.Validate() != nil || candidate.RunID != request.RunID || request.Limit < 1 || request.Limit > projection.MaxReplayBatch || candidate.Enqueued < 0 || candidate.Enqueued > request.Limit ||
			(success && (candidate.Error != "" || (!candidate.Complete && candidate.Enqueued != request.Limit))) ||
			(!success && (candidate.Error == "" || candidate.Complete)) {
			return errors.New("inconsistent projection replay outcome")
		}
		cursor := func(value string) (eventstore.Root, error) {
			var root eventstore.Root
			if err := eventstore.Decode([]byte(value), &root); err != nil {
				return root, err
			}
			if strings.TrimSpace(root.Network) == "" || strings.TrimSpace(root.Service) == "" || strings.TrimSpace(root.Domain) == "" || strings.TrimSpace(root.ID) == "" {
				return root, errors.New("incomplete replay cursor")
			}
			return root, nil
		}
		before := ""
		var previous eventstore.Root
		if request.After != "" {
			var err error
			previous, err = cursor(request.After)
			if err != nil {
				return err
			}
			before = previous.Key()
		}
		if candidate.Enqueued == 0 {
			if candidate.After != before {
				return errors.New("replay cursor advanced without enqueue")
			}
		} else {
			after, err := cursor(candidate.After)
			if err != nil || candidate.After != after.Key() || candidate.After <= before {
				return errors.New("replay cursor did not advance")
			}
			if before != "" && (after.Network != previous.Network || after.Service != previous.Service || after.Domain != previous.Domain) {
				return errors.New("replay cursor changed scope")
			}
		}
		*target = candidate
	default:
		return errors.New("unknown projection acknowledgment")
	}
	return nil
}
