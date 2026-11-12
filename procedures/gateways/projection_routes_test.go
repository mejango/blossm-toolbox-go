package transport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/lib/authentication_error"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/projection"
	"github.com/mejango/blossm-toolbox-go/procedures/view_stores/generic"
	"github.com/mejango/blossm-toolbox-go/rpc/generic"
)

type projectionAggregateFn func(context.Context, eventlib.Root) (eventlib.Aggregate, error)

func (f projectionAggregateFn) Aggregate(ctx context.Context, r eventlib.Root) (eventlib.Aggregate, error) {
	return f(ctx, r)
}

type projectionWriteFn func(context.Context, []view.Mutation) ([]view.Result, error)

func (f projectionWriteFn) Batch(ctx context.Context, m []view.Mutation) ([]view.Result, error) {
	return f(ctx, m)
}

type projectionRootsFn func(context.Context, func(eventlib.Root) error) error

func (f projectionRootsFn) StreamRoots(ctx context.Context, yield func(eventlib.Root) error) error {
	return f(ctx, yield)
}

type projectionEnqueueFn func(context.Context, projection.Request) error

func (f projectionEnqueueFn) Enqueue(ctx context.Context, r projection.Request) error {
	return f(ctx, r)
}

type projectionRoundTrip func(*http.Request) (*http.Response, error)

func (f projectionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProjectionHTTPRPC(t *testing.T) {
	source := projection.Source{Network: "trusted", Service: "service", Domain: "domain"}
	root := func(id string) eventlib.Root {
		return eventlib.Root{Network: source.Network, Service: source.Service, Domain: source.Domain, ID: id}
	}
	aggregates, writes, pushes, enqueued := 0, 0, 0, 0
	var writeErr error
	malformedRows := false
	failEnqueue := false
	engine := &projection.Engine{
		Aggregates: projectionAggregateFn(func(ctx context.Context, r eventlib.Root) (eventlib.Aggregate, error) {
			aggregates++
			if r != root("allowed") {
				t.Fatalf("untrusted root: %#v", r)
			}
			return eventlib.Aggregate{Root: r, Version: 3, State: json.RawMessage(`{}`)}, ctx.Err()
		}),
		Views: projectionWriteFn(func(_ context.Context, m []view.Mutation) ([]view.Result, error) {
			writes++
			if writeErr != nil {
				return nil, writeErr
			}
			if malformedRows {
				return nil, nil
			}
			rows := make([]view.Result, len(m))
			for i, v := range m {
				rows[i] = view.Result{ID: v.ID, Version: v.Version, Applied: true}
			}
			return rows, nil
		}),
		Handlers: map[projection.Source]projection.Handler{source: func(_ context.Context, in projection.Input) (projection.Plan, error) {
			if in.Action != "" {
				t.Fatal("manual caller injected action")
			}
			return projection.Plan{Mutations: []projection.Mutation{{ID: "view", Body: json.RawMessage(`{"owner":"trusted"}`)}}}, nil
		}},
		Push: func(context.Context, projection.Notification) error {
			pushes++
			return errors.New("private push detail")
		},
	}
	scheduler := &projection.Scheduler{
		Roots: projectionRootsFn(func(ctx context.Context, yield func(eventlib.Root) error) error {
			for _, id := range []string{"a", "b", "c"} {
				if err := yield(root(id)); err != nil {
					return err
				}
			}
			return ctx.Err()
		}),
		Producer: projectionEnqueueFn(func(_ context.Context, r projection.Request) error {
			if r.Event != nil {
				t.Fatal("replay enabled push")
			}
			if failEnqueue && r.Root.ID == "b" {
				return errors.New("private queue credentials")
			}
			enqueued++
			return nil
		}),
	}
	routes := ProjectionRoutes{Resolve: func(r *http.Request, op ProjectionOperation) (ProjectionBinding, error) {
		if r.Header.Get("Authorization") != "Bearer fixture" {
			return ProjectionBinding{}, authn.ErrUnauthorized
		}
		if op.Name != "bird" || (!op.Replay && op.RootID != "allowed") {
			return ProjectionBinding{}, authn.ErrForbidden
		}
		return ProjectionBinding{Source: source, Engine: engine, Scheduler: scheduler}, nil
	}}
	handler, err := routes.Handler()
	if err != nil {
		t.Fatal(err)
	}
	transport := projectionRoundTrip(func(r *http.Request) (*http.Response, error) {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	client, err := rpc.New("https://fixture.invalid", rpc.Options{HTTP: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal(err)
	}
	c := ProjectionClient{RPC: client}
	headers := http.Header{"Authorization": {"Bearer fixture"}}
	play, err := c.Play(context.Background(), "bird", ProjectionPlayInput{RootID: "allowed"}, headers)
	if err != nil || !play.Committed || play.Uncertain || len(play.Rows) != 1 || pushes != 0 || aggregates != 1 || writes != 1 {
		t.Fatalf("play: %#v %v", play, err)
	}
	for _, tc := range []struct {
		body   string
		auth   bool
		status int
	}{
		{`{"rootId":"allowed"}`, false, 401},
		{`{"rootId":"forbidden"}`, true, 403},
		{`{"rootId":"allowed","source":{"Network":"foreign"}}`, true, 400},
		{`{"rootId":"allowed","event":{"Action":"push"}}`, true, 400},
		{`{"rootId":"allowed","rootId":"forbidden"}`, true, 400},
		{`{"rootId":""}`, true, 400},
		{`{"rootId":"` + strings.Repeat("x", 1<<20) + `"}`, true, 400},
	} {
		before := aggregates
		r := httptest.NewRequest("POST", "/projections/bird/play", strings.NewReader(tc.body))
		if tc.auth {
			r.Header = headers.Clone()
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status || aggregates != before {
			t.Fatalf("invalid input status %d expected%d calls%d/%d", w.Code, tc.status, aggregates, before)
		}
	}
	writeErr = eventlib.ErrUncertain
	play, err = c.Play(context.Background(), "bird", ProjectionPlayInput{RootID: "allowed"}, headers)
	var httpErr *rpc.HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != 503 || !play.Uncertain || play.Committed {
		t.Fatalf("uncertain outcome: %#v %v", play, err)
	}
	writeErr = nil
	malformedRows = true
	play, err = c.Play(context.Background(), "bird", ProjectionPlayInput{RootID: "allowed"}, headers)
	if err == nil || !play.Committed {
		t.Fatalf("lost committed failure: %#v %v", play, err)
	}
	malformedRows = false
	replay, err := c.Replay(context.Background(), "bird", ProjectionReplayInput{RunID: "run-1", Limit: 2}, headers)
	if err != nil || replay.RunID != "run-1" || replay.Enqueued != 2 || replay.After != root("b").Key() || replay.Complete {
		t.Fatalf("page: %#v %v", replay, err)
	}
	replay, err = c.Replay(context.Background(), "bird", ProjectionReplayInput{RunID: "run-1", After: replay.After, Limit: 2}, headers)
	if err != nil || replay.RunID != "run-1" || replay.Enqueued != 1 || !replay.Complete {
		t.Fatalf("resume: %#v %v", replay, err)
	}
	failEnqueue = true
	replay, err = c.Replay(context.Background(), "bird", ProjectionReplayInput{RunID: "run-1", Limit: 3}, headers)
	if err == nil || replay.Enqueued != 1 || replay.After != root("a").Key() || replay.Complete || strings.Contains(replay.Error, "credentials") {
		t.Fatalf("lost partial progress: %#v %v", replay, err)
	}
	before := enqueued
	for _, in := range []ProjectionReplayInput{{Limit: 1}, {RunID: " ", Limit: 1}, {RunID: "run-1", Limit: 0}, {RunID: "run-1", Limit: projection.MaxReplayBatch + 1}, {RunID: "run-1", Limit: 1, After: `{"network":"foreign","service":"service","domain":"domain","id":"a"}`}} {
		if _, err = c.Replay(context.Background(), "bird", in, headers); err == nil || enqueued != before {
			t.Fatalf("invalid replay: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.Play(ctx, "bird", ProjectionPlayInput{RootID: "allowed"}, headers); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if _, err = (ProjectionRoutes{}).Handler(); err == nil {
		t.Fatal("missing auth accepted")
	}
	if _, err = (ProjectionClient{}).Play(context.Background(), "bird", ProjectionPlayInput{}, headers); err == nil {
		t.Fatal("missing rpc accepted")
	}
	if _, err = c.Play(context.Background(), "../bird", ProjectionPlayInput{}, headers); err == nil {
		t.Fatal("path traversal")
	}
}

func TestProjectionRPCMalformedResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{200, `not json`}, {200, `{"committed":true,"error":"failed"}`}, {204, ``}, {500, `not json`}} {
		client, err := rpc.New("https://fixture.invalid", rpc.Options{HTTP: &http.Client{Transport: projectionRoundTrip(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
		})}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = (ProjectionClient{RPC: client}).Play(context.Background(), "bird", ProjectionPlayInput{RootID: "allowed"}, nil); err == nil {
			t.Fatalf("accepted malformed response %v", tc)
		}
	}
}

func TestProjectionRPCRequiredOutcomes(t *testing.T) {
	root := eventlib.Root{Network: "n", Service: "s", Domain: "d", ID: "a"}
	next := root
	next.ID = "b"
	play := map[string]any{"rows": []any{}, "visited": 1, "ignored": false, "committed": false, "uncertain": false}
	replay := map[string]any{"runId": "run-1", "enqueued": 0, "after": "", "complete": true}
	for _, operation := range []string{"play", "replay"} {
		base := play
		if operation == "replay" {
			base = replay
		}
		cases := []string{`null`, `{}`, `[]`}
		for field := range base {
			missing := map[string]any{}
			for k, v := range base {
				missing[k] = v
			}
			delete(missing, field)
			b, _ := json.Marshal(missing)
			cases = append(cases, string(b))
			missing[field] = nil
			b, _ = json.Marshal(missing)
			cases = append(cases, string(b))
		}
		if operation == "play" {
			cases = append(cases, `{"rows":[],"visited":1,"ignored":false,"committed":true,"uncertain":false}`, `{"rows":[{"id":"x","version":0}],"visited":1,"ignored":false,"committed":true,"uncertain":false}`, `{"rows":[],"visited":1,"ignored":false,"committed":true,"uncertain":true}`, `{"rows":[],"visited":-1,"ignored":false,"committed":false,"uncertain":false}`, `{"rows":[],"visited":1,"ignored":true,"committed":false,"uncertain":false}`, `{"rows":[],"visited":0,"ignored":false,"committed":false,"uncertain":false}`, `{"rows":[],"visited":1,"ignored":false,"committed":false,"uncertain":true}`, `{"rows":[{"id":"x","version":0,"applied":true}],"visited":1,"ignored":false,"committed":false,"uncertain":false}`)
		} else {
			cases = append(cases, `{"runId":"run-1","enqueued":-1,"after":"","complete":true}`, `{"runId":"run-1","enqueued":0,"after":"","complete":false}`, `{"runId":"run-1","enqueued":1,"after":"","complete":true}`, `{"runId":"run-1","enqueued":3,"after":"invalid","complete":true}`)
		}
		for _, body := range cases {
			t.Run(operation+"/"+body, func(t *testing.T) {
				client, e := rpc.New("https://fixture.invalid", rpc.Options{HTTP: &http.Client{Transport: projectionRoundTrip(func(r *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
				})}})
				if e != nil {
					t.Fatal(e)
				}
				if operation == "play" {
					out, e := (ProjectionClient{RPC: client}).Play(context.Background(), "name", ProjectionPlayInput{RootID: "a"}, nil)
					if e == nil || out.Committed || out.Uncertain {
						t.Fatalf("trusted invalid acknowledgment: %#v %v", out, e)
					}
				} else {
					out, e := (ProjectionClient{RPC: client}).Replay(context.Background(), "name", ProjectionReplayInput{RunID: "run-1", Limit: 2}, nil)
					if e == nil || out.Complete || out.Enqueued != 0 {
						t.Fatalf("trusted invalid acknowledgment: %#v %v", out, e)
					}
				}
			})
		}
	}
	// Legitimate no-op/ignored/committed outcomes must not be confused with absent fields.
	for _, body := range []string{`{"rows":[],"visited":0,"ignored":true,"committed":false,"uncertain":false}`, `{"rows":[],"visited":1,"ignored":false,"committed":false,"uncertain":false}`, `{"rows":[{"id":"x","applied":false,"version":-1}],"visited":1,"ignored":false,"committed":true,"uncertain":false}`} {
		var out ProjectionPlayOutput
		if err := decodeProjectionAck([]byte(body), ProjectionPlayInput{}, &out, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		after string
		out   ProjectionReplayOutput
		valid bool
	}{
		{"", ProjectionReplayOutput{Complete: true}, true},
		{"", ProjectionReplayOutput{Enqueued: 2, After: next.Key()}, true},
		{root.Key(), ProjectionReplayOutput{After: root.Key(), Complete: true}, true},
		{root.Key(), ProjectionReplayOutput{Enqueued: 1, After: root.Key(), Complete: true}, false},
		{root.Key(), ProjectionReplayOutput{After: next.Key(), Complete: true}, false},
		{root.Key(), ProjectionReplayOutput{Enqueued: 1, After: `{"network":"other","service":"s","domain":"d","id":"b"}`, Complete: true}, false},
	} {
		tc.out.RunID = "run-1"
		data, _ := json.Marshal(tc.out)
		var out ProjectionReplayOutput
		err := decodeProjectionAck(data, ProjectionReplayInput{RunID: "run-1", After: tc.after, Limit: 2}, &out, true)
		if (err == nil) != tc.valid {
			t.Fatalf("cursor result %#v: %v", tc, err)
		}
	}
}

func TestReplayAcknowledgmentBindsRun(t *testing.T) {
	for _, run := range []projection.RunID{"", "other-run"} {
		raw, err := json.Marshal(ProjectionReplayOutput{RunID: run, Complete: true})
		if err != nil {
			t.Fatal(err)
		}
		var out ProjectionReplayOutput
		if err = decodeProjectionAck(raw, ProjectionReplayInput{RunID: "requested-run", Limit: 1}, &out, true); err == nil || out.RunID != "" || out.Complete {
			t.Fatal("accepted other run progress", out, err)
		}
	}
}
