//go:build integration

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	cloudaws "blossm.local/backend/cloud/aws"
	"blossm.local/backend/command"
	"blossm.local/backend/eventstore"
	mongostore "blossm.local/backend/storage/mongo"
	"blossm.local/backend/storage/postgres"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/jackc/pgx/v5"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestMain(m *testing.M) {
	for _, name := range []string{"POSTGRES_URL", "MONGODB_URI"} {
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u == nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
			fmt.Fprintln(os.Stderr, name+" must name a disposable localhost database; integration tests never skip")
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func nativeDeliveryBackend(t *testing.T, kind string) (eventstore.Backend, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	name := fmt.Sprintf("delivery_test_%d", time.Now().UnixNano())
	if kind == "postgres" {
		b, err := postgres.Open(ctx, os.Getenv("POSTGRES_URL"), name)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			clean, done := context.WithTimeout(context.Background(), 10*time.Second)
			defer done()
			if err := b.Close(clean); err != nil {
				t.Error(err)
			}
			conn, err := pgx.Connect(clean, os.Getenv("POSTGRES_URL"))
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close(clean)
			if _, err = conn.Exec(clean, "DROP SCHEMA "+pgx.Identifier{name}.Sanitize()+" CASCADE"); err != nil {
				t.Error(err)
			}
		})
		return b, ctx
	}
	b, err := mongostore.Open(ctx, mongostore.Config{URI: os.Getenv("MONGODB_URI"), Database: name, Namespace: "delivery"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if err := b.Close(clean); err != nil {
			t.Error(err)
		}
		client, err := driver.Connect(options.Client().ApplyURI(os.Getenv("MONGODB_URI")))
		if err != nil {
			t.Error(err)
			return
		}
		defer client.Disconnect(clean)
		if err = client.Database(name).Drop(clean); err != nil {
			t.Error(err)
		}
	})
	return b, ctx
}

type nativeDeliveryReducer struct{}

func (nativeDeliveryReducer) Normalize(_ string, p json.RawMessage) (json.RawMessage, error) {
	return eventstore.Canonical(p)
}
func (nativeDeliveryReducer) Apply(_ json.RawMessage, e eventstore.Event) (json.RawMessage, error) {
	return e.Input.Payload, nil
}
func nativeDeliveryStore(b eventstore.Backend) *eventstore.Store {
	return &eventstore.Store{Backend: b, Local: true, Reducers: map[string]eventstore.Reducer{"s/d": nativeDeliveryReducer{}}}
}
func nativeSubmission(id string) command.Submission {
	in := eventstore.Input{Root: eventstore.Root{Network: "n", Service: "s", Domain: "d", ID: id}, Action: "create", Key: id, Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"owner":"alice","value":1}`), Transaction: eventstore.Transaction{ID: id, Issued: "2026-11-12T00:00:00Z"}}
	return command.Submission{ID: id, Events: []eventstore.Input{in}}
}
func nativeJournal(b eventstore.Backend, name string) RejectionJournal {
	return RejectionJournal{Backend: b, Scope: eventstore.Root{Network: "n", Service: "s", Domain: "delivery-rejection", ID: name}}
}
func nativeBody(t *testing.T, s command.Submission) ([]byte, Envelope) {
	t.Helper()
	b, err := commandBody(s)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	return b, e
}
func nativePoison(t *testing.T, ctx context.Context, store *eventstore.Store, id string) command.Submission {
	t.Helper()
	s := nativeSubmission(id)
	if _, err := command.Consume(ctx, store, s); err != nil {
		t.Fatal(err)
	}
	s.ID += "-changed-request"
	s.Events[0].Payload = json.RawMessage(`{"owner":"alice","value":2}`)
	return s
}
func nativeQueue(messages []types.Message, ack func(context.Context, string) error) cloudaws.SQS {
	return cloudaws.SQS{Client: sqsAPI{
		receive: func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
			return &sqs.ReceiveMessageOutput{Messages: messages}, nil
		},
		delete: func(ctx context.Context, in *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
			if err := ack(ctx, *in.ReceiptHandle); err != nil {
				return nil, err
			}
			return &sqs.DeleteMessageOutput{}, nil
		},
	}}
}
func nativeMessage(body []byte, receipt string) types.Message {
	return types.Message{Body: sdk.String(string(body)), ReceiptHandle: sdk.String(receipt)}
}
func nativePoll(ctx context.Context, c *Consumer, q cloudaws.SQS) (int, error) {
	return (SQSWorker{Queue: q, Consumer: c}).Poll(ctx, &sqs.ReceiveMessageInput{QueueUrl: sdk.String("https://sqs.local/native.fifo")})
}
func nativeCount(t *testing.T, ctx context.Context, b eventstore.Backend, r eventstore.Root, want int64) {
	t.Helper()
	err := b.View(ctx, func(reader eventstore.Reader) error {
		count, err := reader.Count(ctx, r)
		if err == nil && count != want {
			return fmt.Errorf("count %d, want %d", count, want)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeRejectionJournal(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			b, ctx := nativeDeliveryBackend(t, kind)
			store := nativeDeliveryStore(b)
			t.Run("poison-followed-valid-and-duplicate", func(t *testing.T) { nativePoisonProgress(t, ctx, b, store) })
			t.Run("uncertain-committed-journal", func(t *testing.T) { nativeUncertainJournal(t, ctx, b, store) })
			t.Run("canceled-journal-commit", func(t *testing.T) { nativeCanceledJournal(t, ctx, b, store) })
			t.Run("canceled-event-commit", func(t *testing.T) { nativeCanceledCommand(t, ctx, b, store) })
		})
	}
}

func nativePoisonProgress(t *testing.T, ctx context.Context, b eventstore.Backend, store *eventstore.Store) {
	journal := nativeJournal(b, "ordered")
	poison := nativePoison(t, ctx, store, "poison")
	body, e := nativeBody(t, poison)
	following := nativeSubmission("following")
	next, _ := nativeBody(t, following)
	calls := 0
	observed := 0
	c := Consumer{Admit: admit, Rejections: journal, Commands: appender(func(ctx context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		calls++
		return store.Append(ctx, in)
	})}
	c.OnRejected = func(ctx context.Context, r Rejection) error {
		persisted, err := journal.Lookup(ctx, r.EnvelopeID)
		if err != nil || persisted != r {
			t.Fatal("observer before native commit", persisted, err)
		}
		observed++
		return nil
	}
	var acked []string
	queue := nativeQueue([]types.Message{nativeMessage(body, "poison-receipt"), nativeMessage(next, "valid-receipt")}, func(ctx context.Context, receipt string) error {
		if receipt == "poison-receipt" {
			record, err := journal.Lookup(ctx, e.ID)
			if err != nil {
				return err
			}
			if record.Reason != RejectionIdempotency || observed != 1 {
				return fmt.Errorf("wrong rejection %v", record)
			}
		} else {
			nativeCount(t, ctx, b, following.Events[0].Root, 1)
		}
		acked = append(acked, receipt)
		return nil
	})
	n, err := nativePoll(ctx, &c, queue)
	if err != nil || n != 2 || !reflect.DeepEqual(acked, []string{"poison-receipt", "valid-receipt"}) {
		t.Fatal(n, err, acked)
	}
	record := Rejection{EnvelopeID: e.ID, Reason: RejectionIdempotency}
	if got, fresh, err := journal.Record(ctx, record); err != nil || got != record || fresh {
		t.Fatal(got, err)
	}
	result, err := c.Consume(ctx, body)
	if err != nil || result.Rejected == nil || *result.Rejected != record || calls != 2 || observed != 1 {
		t.Fatal(result, err, calls)
	}
	other := journal
	other.Scope.ID = "other-consumer"
	if _, err = other.Lookup(ctx, e.ID); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("cross-scope record", err)
	}
	changed := record
	changed.Reason = RejectionConflict
	if _, _, err = journal.Record(ctx, changed); !errors.Is(err, ErrRejectionMismatch) {
		t.Fatal("record overwritten", err)
	}
	key, root, err := journal.location(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	err = b.View(ctx, func(reader eventstore.Reader) error {
		p, err := reader.Projection(ctx, key)
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(p.Body, &fields); err != nil {
			return err
		}
		if p.Root != root || p.Version != 0 || p.Deleted || len(fields) != 2 || fields["envelopeId"] == nil || fields["reason"] == nil {
			return fmt.Errorf("unexpected journal representation")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	nativeCount(t, ctx, b, poison.Events[0].Root, 1)
}

// The underlying Update really commits. Only its acknowledgment is altered;
// this never touches server failpoints or changes the adapter retry algorithm.
type nativeJournalAck struct {
	eventstore.Backend
	after func() error
}

func (w nativeJournalAck) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	if err := w.Backend.Update(ctx, fn); err != nil {
		return err
	}
	return w.after()
}
func nativeUncertainJournal(t *testing.T, ctx context.Context, b eventstore.Backend, store *eventstore.Store) {
	journal := nativeJournal(b, "uncertain")
	body, e := nativeBody(t, nativePoison(t, ctx, store, "uncertain-poison"))
	following := nativeSubmission("uncertain-follower")
	next, _ := nativeBody(t, following)
	var lose atomic.Bool
	lose.Store(true)
	wrapper := nativeJournalAck{Backend: b, after: func() error {
		if lose.CompareAndSwap(true, false) {
			return eventstore.ErrUncertain
		}
		return nil
	}}
	uncertain := journal
	uncertain.Backend = wrapper
	calls := 0
	observed := 0
	c := Consumer{Admit: admit, Rejections: uncertain, Commands: appender(func(ctx context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		calls++
		return store.Append(ctx, in)
	})}
	c.OnRejected = func(context.Context, Rejection) error { observed++; return nil }
	acks := 0
	queue := nativeQueue([]types.Message{nativeMessage(body, "uncertain"), nativeMessage(next, "following")}, func(context.Context, string) error { acks++; return nil })
	n, err := nativePoll(ctx, &c, queue)
	if n != 0 || acks != 0 || calls != 1 || observed != 0 || !errors.Is(err, eventstore.ErrUncertain) || !errors.Is(err, eventstore.ErrIdempotency) {
		t.Fatal(n, err, acks, calls)
	}
	if record, err := journal.Lookup(ctx, e.ID); err != nil || record.Reason != RejectionIdempotency {
		t.Fatal("real commit missing", record, err)
	}
	nativeCount(t, ctx, b, following.Events[0].Root, 0)
	n, err = nativePoll(ctx, &c, queue)
	if err != nil || n != 2 || acks != 2 || calls != 2 || observed != 0 {
		t.Fatal("retry failed to recover exact record", n, err, acks, calls)
	}
	nativeCount(t, ctx, b, following.Events[0].Root, 1)
}
func nativeCanceledJournal(t *testing.T, ctx context.Context, b eventstore.Backend, store *eventstore.Store) {
	journal := nativeJournal(b, "canceled-journal")
	body, e := nativeBody(t, nativePoison(t, ctx, store, "canceled-journal-poison"))
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	wrapped := journal
	wrapped.Backend = nativeJournalAck{Backend: b, after: func() error { cancel(); return nil }}
	c := Consumer{Admit: admit, Rejections: wrapped, Commands: store}
	acks := 0
	queue := nativeQueue([]types.Message{nativeMessage(body, "cancel")}, func(context.Context, string) error { acks++; return nil })
	n, err := nativePoll(canceled, &c, queue)
	if n != 0 || acks != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal(n, err, acks)
	}
	if _, err = journal.Lookup(ctx, e.ID); err != nil {
		t.Fatal("journal commit rolled back", err)
	}
	c.Rejections = journal
	n, err = nativePoll(ctx, &c, queue)
	if err != nil || n != 1 || acks != 1 {
		t.Fatal("retry did not confirm committed rejection", n, err, acks)
	}
}
func nativeCanceledCommand(t *testing.T, ctx context.Context, b eventstore.Backend, store *eventstore.Store) {
	journal := nativeJournal(b, "canceled-command")
	s := nativeSubmission("canceled-command")
	body, e := nativeBody(t, s)
	canceled, cancel := context.WithCancel(ctx)
	defer cancel()
	c := Consumer{Admit: admit, Rejections: journal, Commands: appender(func(ctx context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		receipts, err := store.Append(ctx, in)
		if err == nil {
			cancel()
		}
		return receipts, err
	})}
	acks := 0
	queue := nativeQueue([]types.Message{nativeMessage(body, "cancel-event")}, func(context.Context, string) error { acks++; return nil })
	n, err := nativePoll(canceled, &c, queue)
	if n != 0 || acks != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal(n, err, acks)
	}
	nativeCount(t, ctx, b, s.Events[0].Root, 1)
	if _, err = journal.Lookup(ctx, e.ID); !errors.Is(err, eventstore.ErrNotFound) {
		t.Fatal("successful event marked rejected", err)
	}
	c.Commands = store
	n, err = nativePoll(ctx, &c, queue)
	if err != nil || n != 1 || acks != 1 {
		t.Fatal(n, err, acks)
	}
	nativeCount(t, ctx, b, s.Events[0].Root, 1)
}
