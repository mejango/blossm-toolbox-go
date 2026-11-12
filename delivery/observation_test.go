package delivery

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	cloudaws "blossm.local/backend/cloud/aws"
	"blossm.local/backend/cloud/gcp"
	"blossm.local/backend/command"
	"blossm.local/backend/eventstore"
	tasks "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

func TestAcceptanceExposesStableEnvelopeID(t *testing.T) {
	var expected string
	tasksTarget := TasksTarget{Queue: "projects/p/locations/l/queues/q", Dispatch: gcp.HTTPDispatch{URL: "https://worker.test/consume", BearerToken: "trusted"}}
	tasksTarget.Tasks.Client = tasksAPI{create: func(_ context.Context, in *tasks.CreateTaskRequest) (*tasks.Task, error) {
		e, err := Decode(in.Task.GetHttpRequest().Body)
		if err != nil {
			t.Fatal(err)
		}
		expected = e.ID
		return in.Task, nil
	}}
	sqsTarget := SQSTarget{QueueURL: "https://sqs.test/queue", Queue: cloudaws.SQS{Client: sqsAPI{send: func(_ context.Context, in *sqs.SendMessageInput) (*sqs.SendMessageOutput, error) {
		e, err := Decode([]byte(*in.MessageBody))
		if err != nil {
			t.Fatal(err)
		}
		expected = e.ID
		return &sqs.SendMessageOutput{MessageId: sdk.String("provider-id")}, nil
	}}}}
	for _, queue := range []command.Queue{TasksCommands{Target: tasksTarget}, SQSCommands{Target: sqsTarget}} {
		var previous string
		for i := 0; i < 2; i++ {
			a, err := queue.Enqueue(context.Background(), submission())
			if err != nil || a.EnvelopeID != expected || a.ID == "" || a.ID == a.EnvelopeID {
				t.Fatal(a, err, expected)
			}
			if i > 0 && previous != a.EnvelopeID {
				t.Fatal("identity changed")
			}
			previous = a.EnvelopeID
		}
	}
}

func TestRejectionObserverAfterCommitBeforeAck(t *testing.T) {
	_, journal := journalFixture()
	body, e := envelopeFixture(t)
	var steps []string
	observerFailure := errors.New("observer unavailable")
	c := Consumer{Admit: admit, Rejections: journal, Commands: appender(func(context.Context, []eventstore.Input) ([]eventstore.Receipt, error) {
		return nil, eventstore.ErrInvalid
	}), OnRejected: func(ctx context.Context, r Rejection) error {
		got, err := journal.Lookup(ctx, r.EnvelopeID)
		if err != nil || got != r {
			t.Fatal("observer before commit", got, err)
		}
		steps = append(steps, "observed:"+r.EnvelopeID)
		return observerFailure
	}}
	queue := cloudaws.SQS{Client: sqsAPI{receive: func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
		return &sqs.ReceiveMessageOutput{Messages: []types.Message{{Body: sdk.String(string(body)), ReceiptHandle: sdk.String("receipt")}}}, nil
	}, delete: func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
		steps = append(steps, "ack")
		return &sqs.DeleteMessageOutput{}, nil
	}}}
	n, err := (SQSWorker{Queue: queue, Consumer: &c}).Poll(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: sdk.String("queue")})
	if err != nil || n != 1 || !reflect.DeepEqual(steps, []string{"observed:" + e.ID, "ack"}) {
		t.Fatal(n, err, steps)
	}
	// Direct consumers can inspect observer failure separately from durable success.
	next := submission()
	next.ID = "another-envelope"
	nextBody, _ := commandBody(next)
	result, err := c.Consume(context.Background(), nextBody)
	if err != nil || result.Rejected == nil || !errors.Is(result.ObservationError, observerFailure) {
		t.Fatal(result, err)
	}
	before := len(steps)
	result, err = c.Consume(context.Background(), nextBody)
	if err != nil || result.Rejected == nil || result.ObservationError != nil || len(steps) != before {
		t.Fatal("duplicate observation", result, err, steps)
	}
}

func TestRejectionObserverConcurrentDuplicates(t *testing.T) {
	_, journal := journalFixture()
	body, _ := envelopeFixture(t)
	var observed atomic.Int64
	c := Consumer{Admit: admit, Rejections: journal, Commands: appender(func(context.Context, []eventstore.Input) ([]eventstore.Receipt, error) {
		return nil, eventstore.ErrConflict
	}), OnRejected: func(ctx context.Context, r Rejection) error {
		if _, err := journal.Lookup(ctx, r.EnvelopeID); err != nil {
			return err
		}
		observed.Add(1)
		return nil
	}}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			result, err := c.Consume(context.Background(), body)
			if err == nil && (result.Rejected == nil || result.ObservationError != nil) {
				err = errors.New("unconfirmed rejection")
			}
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if observed.Load() != 1 {
		t.Fatal("fresh concurrent observation count", observed.Load())
	}
}

func TestRejectionObserverUncertainCommitDoesNotEmit(t *testing.T) {
	b, journal := journalFixture()
	b.commitErr = eventstore.ErrUncertain
	b.commitDespiteError = true
	body, _ := envelopeFixture(t)
	observed := 0
	c := Consumer{Admit: admit, Rejections: journal, Commands: appender(func(context.Context, []eventstore.Input) ([]eventstore.Receipt, error) {
		return nil, eventstore.ErrConflict
	}), OnRejected: func(context.Context, Rejection) error { observed++; return nil }}
	result, err := c.Consume(context.Background(), body)
	if !errors.Is(err, eventstore.ErrUncertain) || result.Rejected != nil || observed != 0 {
		t.Fatal(result, err, observed)
	}
	b.commitErr = nil
	result, err = c.Consume(context.Background(), body)
	if err != nil || result.Rejected == nil || observed != 0 {
		t.Fatal("lookup duplicate emitted fresh notice", result, err, observed)
	}
}

type retryingJournal struct{ eventstore.Backend }

func (b retryingJournal) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	// First callback inserts, but that attempt is discarded. Another worker then
	// commits the same record before the owning adapter repeats this callback.
	discarded := &journalTx{rows: map[string]eventstore.Projection{}}
	if err := fn(discarded); err != nil {
		return err
	}
	if err := b.Backend.Update(ctx, func(tx eventstore.Tx) error {
		for _, p := range discarded.rows {
			if err := tx.LockRoots(ctx, []eventstore.Root{p.Root}); err != nil {
				return err
			}
			if _, err := tx.PutProjection(ctx, p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return b.Backend.Update(ctx, fn)
}
func TestJournalFreshnessResetsOnTransactionRetry(t *testing.T) {
	b, journal := journalFixture()
	journal.Backend = retryingJournal{Backend: b}
	_, e := envelopeFixture(t)
	record := Rejection{EnvelopeID: e.ID, Reason: RejectionInvalid}
	got, fresh, err := journal.Record(context.Background(), record)
	if err != nil || got != record || fresh {
		t.Fatal("aborted insertion leaked freshness", got, fresh, err)
	}
}
