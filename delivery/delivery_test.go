package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	tasks "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/googleapis/gax-go/v2"
	cloudaws "github.com/mejango/blossm-toolbox-go/cloud/aws"
	"github.com/mejango/blossm-toolbox-go/cloud/gcp"
	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/projection"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type tasksAPI struct {
	gcp.TasksAPI
	create func(context.Context, *tasks.CreateTaskRequest) (*tasks.Task, error)
}

func (f tasksAPI) CreateTask(ctx context.Context, in *tasks.CreateTaskRequest, _ ...gax.CallOption) (*tasks.Task, error) {
	return f.create(ctx, in)
}

type sqsAPI struct {
	cloudaws.SQSAPI
	send    func(context.Context, *sqs.SendMessageInput) (*sqs.SendMessageOutput, error)
	receive func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error)
	delete  func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error)
}

func (f sqsAPI) SendMessage(ctx context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	return f.send(ctx, in)
}
func (f sqsAPI) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return f.receive(ctx, in)
}
func (f sqsAPI) DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return f.delete(ctx, in)
}

type appender func(context.Context, []eventstore.Input) ([]eventstore.Receipt, error)

func (f appender) Append(ctx context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
	return f(ctx, in)
}
func submission() command.Submission {
	return command.Submission{ID: "request", Events: []eventstore.Input{{Root: eventstore.Root{Network: "n", Service: "s", Domain: "d", ID: "r"}, Action: "create", Key: "event", Payload: json.RawMessage(`{"x":1}`)}}}
}
func admit(_ context.Context, e Envelope) error {
	if e.Command != nil {
		for _, in := range e.Command.Events {
			if in.Root.Network != "n" || in.Root.Service != "s" || in.Root.Domain != "d" || in.Action != "create" {
				return command.ErrForbidden
			}
		}
	}
	return nil
}
func receipts(in []eventstore.Input, outcome eventstore.Outcome) []eventstore.Receipt {
	result := make([]eventstore.Receipt, len(in))
	for i, v := range in {
		result[i] = eventstore.Receipt{Outcome: outcome, Event: eventstore.Event{Input: v}}
	}
	return result
}

func TestTasksStableAcceptanceAndUncertainty(t *testing.T) {
	ctx := context.Background()
	var names []string
	var bodies [][]byte
	var failure error
	q := TasksCommands{Target: TasksTarget{Queue: "projects/p/locations/l/queues/q", Dispatch: gcp.HTTPDispatch{URL: "https://worker.test/consume", ServiceAccount: "worker@p.test", Audience: "worker"}}}
	q.Target.Tasks.Client = tasksAPI{create: func(_ context.Context, in *tasks.CreateTaskRequest) (*tasks.Task, error) {
		h := in.Task.GetHttpRequest()
		if h.HttpMethod != tasks.HttpMethod_POST || h.GetOidcToken().Audience != "worker" {
			t.Fatal(h)
		}
		names = append(names, in.Task.Name)
		bodies = append(bodies, h.Body)
		return in.Task, failure
	}}
	for _, f := range []error{nil, status.Error(codes.AlreadyExists, "exists")} {
		failure = f
		a, err := q.Enqueue(ctx, submission())
		if err != nil || a.ID == "" {
			t.Fatal(a, err)
		}
	}
	if names[0] != names[1] || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatal("unstable retry")
	}
	failure = status.Error(codes.Unavailable, "ack lost")
	_, err := q.Enqueue(ctx, submission())
	if !errors.Is(err, ErrEnqueueUncertain) || !errors.Is(err, command.ErrQueueUncertain) {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	before := len(names)
	if _, err = q.Enqueue(canceled, submission()); !errors.Is(err, context.Canceled) || len(names) != before {
		t.Fatal(err)
	}
}

func TestReplayRunIdentityAndTriggerRejection(t *testing.T) {
	r := projection.Request{RunID: "run1", Root: submission().Events[0].Root, Flag: "rebuild"}
	a, err := replayBody(r)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := replayBody(r)
	r.RunID = "run2"
	c, _ := replayBody(r)
	if !bytes.Equal(a, b) || bytes.Equal(a, c) {
		t.Fatal("run identity")
	}
	r.RunID = ""
	if _, err = replayBody(r); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	r.RunID = "run1"
	r.Event = &projection.Trigger{Root: r.Root, Version: 1, Action: "push"}
	if _, err = replayBody(r); !errors.Is(err, ErrInvalid) {
		t.Fatal("untrusted trigger", err)
	}
}

func TestEnvelopeStrictBoundsAndDetachedAdmission(t *testing.T) {
	body, err := commandBody(submission())
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(body, []byte(`"version":1`), []byte(`"version":1,"extra":true`), 1)
	mutated := bytes.Replace(body, []byte(`"x":1`), []byte(`"x":2`), 1)

	for _, bad := range [][]byte{append(append([]byte{}, body...), []byte(` {}`)...), unknown, mutated, bytes.Repeat([]byte{' '}, MaxEnvelopeBytes+1)} {
		if _, err := Decode(bad); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted malformed", err)
		}
	}

	huge := submission()
	huge.Events[0].Payload, _ = json.Marshal(strings.Repeat("a", MaxEnvelopeBytes))
	if _, err = commandBody(huge); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	c := Consumer{Admit: func(_ context.Context, e Envelope) error { e.Command.Events[0].Action = "injected"; return nil }, Commands: appender(func(_ context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		if in[0].Action != "create" {
			t.Fatal("admission mutated effect")
		}
		return receipts(in, eventstore.Committed), nil
	})}
	if _, err = c.Consume(context.Background(), body); err != nil {
		t.Fatal(err)
	}
	c.Admit = nil
	if _, err = c.Consume(context.Background(), body); err == nil {
		t.Fatal("missing policy")
	}
}

func TestHTTPAuthenticationScopeAndDurableAck(t *testing.T) {
	body, _ := commandBody(submission())
	calls := 0
	var failure error
	c := Consumer{Admit: admit, Commands: appender(func(_ context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		calls++
		if failure != nil {
			return nil, failure
		}
		return receipts(in, eventstore.Duplicate), nil
	})}
	if _, err := c.Handler(nil); err == nil {
		t.Fatal("nil authentication")
	}
	h, err := c.Handler(func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer trusted" {
			return command.ErrForbidden
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(payload []byte, token string) int {
		r := httptest.NewRequest("POST", "/consume", bytes.NewReader(payload))
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if got := request(body, ""); got != 401 || calls != 0 {
		t.Fatal(got, calls)
	}
	foreign := submission()
	foreign.Events[0].Root.Network = "foreign"
	bad, _ := commandBody(foreign)
	if got := request(bad, "Bearer trusted"); got/100 == 2 || calls != 0 {
		t.Fatal("scope bypass", got, calls)
	}
	for i := 0; i < 2; i++ {
		if got := request(body, "Bearer trusted"); got != 204 {
			t.Fatal(got)
		}
	}
	failure = eventstore.ErrUncertain
	if got := request(body, "Bearer trusted"); got != 503 {
		t.Fatal(got)
	}
	if got := request(bytes.Repeat([]byte{'a'}, MaxEnvelopeBytes+1), "Bearer trusted"); got != 413 {
		t.Fatal(got)
	}
}

func TestSQSStableFIFOAndStandard(t *testing.T) {
	var requests []*sqs.SendMessageInput
	var failure error
	q := SQSCommands{Target: SQSTarget{QueueURL: "https://sqs.test/q.fifo", GroupID: "group"}}
	q.Target.Queue.Client = sqsAPI{send: func(_ context.Context, in *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) {
		requests = append(requests, in)
		return &sqs.SendMessageOutput{MessageId: sdk.String("accepted")}, failure
	}}
	for i := 0; i < 2; i++ {
		if _, err := q.Enqueue(context.Background(), submission()); err != nil {
			t.Fatal(err)
		}
	}
	if *requests[0].MessageDeduplicationId != *requests[1].MessageDeduplicationId || *requests[0].MessageGroupId != "group" {
		t.Fatal(requests)
	}
	q.Target.QueueURL = "https://sqs.test/q"
	q.Target.GroupID = ""
	if _, err := q.Enqueue(context.Background(), submission()); err != nil {
		t.Fatal(err)
	}
	if requests[2].MessageDeduplicationId != nil || requests[2].MessageGroupId != nil {
		t.Fatal("FIFO options on standard queue")
	}
	failure = errors.New("lost ack")
	if _, err := q.Enqueue(context.Background(), submission()); !errors.Is(err, ErrEnqueueUncertain) {
		t.Fatal(err)
	}
}

func TestSQSReceiptsOnlyAfterDurableSuccess(t *testing.T) {
	for _, mode := range []string{"duplicate", "commit-error", "uncertain", "cancel-after-commit", "bad-receipt", "ack-error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body, _ := commandBody(submission())
			var sequence []string
			c := Consumer{Admit: admit, Commands: appender(func(_ context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
				sequence = append(sequence, "consume")
				switch mode {
				case "commit-error":
					return nil, errors.New("failed")
				case "uncertain":
					return nil, eventstore.ErrUncertain
				case "cancel-after-commit":
					cancel()
				case "bad-receipt":
					return receipts(in, eventstore.Queued), nil
				}
				return receipts(in, eventstore.Duplicate), nil
			})}
			queue := cloudaws.SQS{Client: sqsAPI{receive: func(_ context.Context, in *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
				return &sqs.ReceiveMessageOutput{Messages: []types.Message{{Body: sdk.String(string(body)), ReceiptHandle: sdk.String("receive-specific")}}}, nil
			}, delete: func(_ context.Context, in *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
				sequence = append(sequence, "ack")
				if *in.ReceiptHandle != "receive-specific" {
					t.Fatal(in)
				}
				if mode == "ack-error" {
					return nil, errors.New("ack lost")
				}
				return &sqs.DeleteMessageOutput{}, nil
			}}}
			w := SQSWorker{Queue: queue, Consumer: &c}
			n, err := w.Poll(ctx, &sqs.ReceiveMessageInput{QueueUrl: sdk.String("queue")})
			if mode == "duplicate" {
				if err != nil || n != 1 || !reflect.DeepEqual(sequence, []string{"consume", "ack"}) {
					t.Fatal(n, err, sequence)
				}
			} else {
				if err == nil || n != 0 {
					t.Fatal(n, err)
				}
				if mode != "ack-error" && !reflect.DeepEqual(sequence, []string{"consume"}) {
					t.Fatal(sequence)
				}
			}
		})
	}
}

func TestPublicationIdentityExcludesOutboxAttempts(t *testing.T) {
	e := eventstore.Event{Input: submission().Events[0], Hash: strings.Repeat("a", 64)}
	p := eventstore.Publication{ID: e.Hash, Event: e}
	a, err := publicationBody(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Attempts = 9
	b, err := publicationBody(p)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatal("attempt identity changed", err)
	}
}

func TestTasksPublicationPreservesWorstCaseAcceptedPayload(t *testing.T) {
	in := submission().Events[0]
	in.Payload, _ = json.Marshal(strings.Repeat("<", (230<<10)/6))
	f, err := eventstore.Digest(in)
	if err != nil {
		t.Fatal(err)
	}
	event := eventstore.Event{Input: in, Number: 0, Fingerprint: f}
	event.Hash, err = eventstore.Digest(event)
	if err != nil {
		t.Fatal(err)
	}
	p := eventstore.Publication{ID: event.Hash, Event: event}
	if err = eventstore.ValidatePublicationSize(p); err != nil {
		t.Fatal(err)
	}
	called := false
	q := TasksPublications{Target: TasksTarget{Queue: "projects/p/locations/l/queues/q", Dispatch: gcp.HTTPDispatch{URL: "https://worker.test/consume", BearerToken: "trusted"}}}
	q.Target.Tasks.Client = tasksAPI{create: func(_ context.Context, in *tasks.CreateTaskRequest) (*tasks.Task, error) {
		called = true
		body := in.Task.GetHttpRequest().Body
		if len(body) > MaxEnvelopeBytes {
			t.Fatal("expanded wire body", len(body))
		}
		decoded, err := Decode(body)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Publication.Event.Hash != event.Hash {
			t.Fatal("hash changed")
		}
		if err = eventstore.Verify(decoded.Publication.Event); err != nil {
			t.Fatal(err)
		}
		return in.Task, nil
	}}
	if err = q.Publish(context.Background(), p); err != nil || !called {
		t.Fatal(err, called)
	}
}

func TestTasksRouteIdentityAndActualHTTPConsumption(t *testing.T) {
	count := 0
	c := Consumer{Admit: admit, Commands: appender(func(_ context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		count++
		return receipts(in, eventstore.Committed), nil
	})}
	h, err := c.Handler(func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer trusted" {
			return command.ErrForbidden
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	q := TasksCommands{Target: TasksTarget{Queue: "projects/p/locations/l/queues/q", Dispatch: gcp.HTTPDispatch{URL: "https://worker.test/one", BearerToken: "trusted"}}}
	q.Target.Tasks.Client = tasksAPI{create: func(_ context.Context, in *tasks.CreateTaskRequest) (*tasks.Task, error) {
		names = append(names, in.Task.Name)
		wire := in.Task.GetHttpRequest()
		req := httptest.NewRequest("POST", wire.Url, bytes.NewReader(wire.Body))
		for k, v := range wire.Headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
		return in.Task, nil
	}}
	if _, err = q.Enqueue(context.Background(), submission()); err != nil {
		t.Fatal(err)
	}
	q.Target.Dispatch.URL = "https://worker.test/two"
	if _, err = q.Enqueue(context.Background(), submission()); err != nil {
		t.Fatal(err)
	}
	if names[0] == names[1] || count != 2 {
		t.Fatal("different destinations collided", names, count)
	}
}

func TestProviderReplayAndPublicationBindings(t *testing.T) {
	ctx := context.Background()
	var taskEnvelopes, sqsEnvelopes []Envelope
	target := TasksTarget{Queue: "projects/p/locations/l/queues/q", Dispatch: gcp.HTTPDispatch{URL: "https://worker.test/consume", BearerToken: "trusted"}}
	target.Tasks.Client = tasksAPI{create: func(_ context.Context, in *tasks.CreateTaskRequest) (*tasks.Task, error) {
		e, err := Decode(in.Task.GetHttpRequest().Body)
		if err != nil {
			t.Fatal(err)
		}
		taskEnvelopes = append(taskEnvelopes, e)
		return in.Task, nil
	}}
	sqsTarget := SQSTarget{QueueURL: "https://sqs.test/q", Queue: cloudaws.SQS{Client: sqsAPI{send: func(_ context.Context, in *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) {
		e, err := Decode([]byte(*in.MessageBody))
		if err != nil {
			t.Fatal(err)
		}
		sqsEnvelopes = append(sqsEnvelopes, e)
		return &sqs.SendMessageOutput{MessageId: sdk.String("ack")}, nil
	}}}}
	request := projection.Request{RunID: "run", Root: submission().Events[0].Root}
	for _, producer := range []projection.Producer{TasksReplay{Target: target}, SQSReplay{Target: sqsTarget}} {
		if err := producer.Enqueue(ctx, request); err != nil {
			t.Fatal(err)
		}
	}
	event := eventstore.Event{Input: submission().Events[0], Hash: strings.Repeat("a", 64)}
	p := eventstore.Publication{ID: event.Hash, Event: event, Attempts: 3}
	for _, publisher := range []eventstore.Publisher{TasksPublications{Target: target}, SQSPublications{Target: sqsTarget}} {
		if err := publisher.Publish(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(taskEnvelopes, sqsEnvelopes) || len(taskEnvelopes) != 2 || taskEnvelopes[0].Replay.RunID != "run" || taskEnvelopes[0].Replay.Event != nil || taskEnvelopes[1].Publication.Attempts != 0 {
		t.Fatal(taskEnvelopes, sqsEnvelopes)
	}
}

func TestHTTPCancellationAfterCommitDoesNotAcknowledge(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	committed := false
	c := Consumer{Admit: admit, Commands: appender(func(_ context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		committed = true
		cancel()
		return receipts(in, eventstore.Committed), nil
	})}
	h, err := c.Handler(func(*http.Request) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	body, _ := commandBody(submission())
	r := httptest.NewRequest("POST", "/consume", bytes.NewReader(body)).WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if !committed || w.Code/100 == 2 {
		t.Fatal(committed, w.Code)
	}
}
