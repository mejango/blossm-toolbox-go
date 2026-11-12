package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	cloudaws "github.com/mejango/blossm-toolbox-go/vendors/aws"
)

// Journal fixture implements only the transaction primitives exercised here.
// Native parent composition checks the same journal on both real adapters.
type journalBackend struct {
	eventlib.Backend
	mu                 sync.Mutex
	rows               map[string]eventlib.Projection
	commitErr          error
	commitDespiteError bool
}
type journalTx struct {
	eventlib.Tx
	rows   map[string]eventlib.Projection
	locked bool
}

func (tx *journalTx) LockRoots(context.Context, []eventlib.Root) error {
	tx.locked = true
	return nil
}
func (tx *journalTx) Projection(_ context.Context, id string) (eventlib.Projection, error) {
	p, ok := tx.rows[id]
	if !ok {
		return p, eventlib.ErrNotFound
	}
	return p, nil
}
func (tx *journalTx) PutProjection(_ context.Context, p eventlib.Projection) (bool, error) {
	if !tx.locked {
		return false, errors.New("journal wrote before lock")
	}
	if old, ok := tx.rows[p.ID]; ok {
		if old.Root != p.Root {
			return false, eventlib.ErrConflict
		}
		if old.Version >= p.Version {
			return false, nil
		}
	}
	tx.rows[p.ID] = p
	return true, nil
}
func (b *journalBackend) View(ctx context.Context, fn func(eventlib.Reader) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return fn(&journalTx{rows: b.rows})
}
func (b *journalBackend) Update(ctx context.Context, fn func(eventlib.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	rows := map[string]eventlib.Projection{}
	for k, v := range b.rows {
		rows[k] = v
	}
	if err := fn(&journalTx{rows: rows}); err != nil {
		return err
	}
	if b.commitErr == nil || b.commitDespiteError {
		b.rows = rows
	}
	return b.commitErr
}
func journalFixture() (*journalBackend, RejectionJournal) {
	b := &journalBackend{rows: map[string]eventlib.Projection{}}
	return b, RejectionJournal{Backend: b, Scope: eventlib.Root{Network: "n", Service: "s", Domain: "delivery-rejection", ID: "commands"}}
}
func envelopeFixture(t *testing.T) ([]byte, Envelope) {
	t.Helper()
	body, err := commandBody(submission())
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	return body, e
}

func TestJournalImmutableScopedRecordAndMinimalBody(t *testing.T) {
	b, j := journalFixture()
	_, e := envelopeFixture(t)
	record := Rejection{EnvelopeID: e.ID, Reason: RejectionForbidden}
	if _, err := j.Lookup(context.Background(), e.ID); !errors.Is(err, eventlib.ErrNotFound) {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, fresh, err := j.Record(context.Background(), record)
		if err != nil || got != record || fresh != (i == 0) {
			t.Fatal(got, err)
		}
	}
	got, err := j.Lookup(context.Background(), e.ID)
	if err != nil || got != record {
		t.Fatal(got, err)
	}
	changed := record
	changed.Reason = RejectionConflict
	if _, _, err = j.Record(context.Background(), changed); !errors.Is(err, ErrRejectionMismatch) {
		t.Fatal("overwritten rejection", err)
	}
	other := j
	other.Scope.ID = "another-consumer"
	if _, err = other.Lookup(context.Background(), e.ID); !errors.Is(err, eventlib.ErrNotFound) {
		t.Fatal("scope leak", err)
	}
	if len(b.rows) != 1 {
		t.Fatal(b.rows)
	}
	for _, p := range b.rows {
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(p.Body, &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 2 || fields["envelopeId"] == nil || fields["reason"] == nil {
			t.Fatal("private data retained", fields)
		}
		if p.Version != 0 || !strings.HasPrefix(p.ID, "view:") {
			t.Fatal("not internal immutable named record", p)
		}
	}
}

func TestJournalFailureAndLostAckRecovery(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(map[bool]string{false: "rollback", true: "uncertain-committed"}[committed], func(t *testing.T) {
			b, j := journalFixture()
			body, e := envelopeFixture(t)
			b.commitErr = eventlib.ErrUncertain
			b.commitDespiteError = committed
			calls := 0
			c := Consumer{Admit: admit, Rejections: j, Commands: appender(func(context.Context, []eventlib.Input) ([]eventlib.Receipt, error) {
				calls++
				return nil, eventlib.ErrIdempotency
			})}
			result, err := c.Consume(context.Background(), body)
			if !errors.Is(err, eventlib.ErrUncertain) || !errors.Is(err, eventlib.ErrIdempotency) || result.Rejected != nil {
				t.Fatal(result, err)
			}
			b.commitErr = nil
			b.commitDespiteError = false
			result, err = c.Consume(context.Background(), body)
			if err != nil || result.Rejected == nil || result.Rejected.EnvelopeID != e.ID {
				t.Fatal(result, err)
			}
			expected := 2
			if committed {
				expected = 1
			}
			if calls != expected {
				t.Fatal("wrong duplicate recovery", calls)
			}
		})
	}
}

type labeledDeliveryError struct{ labels []string }

func (e labeledDeliveryError) Error() string { return "provider failure" }
func (e labeledDeliveryError) HasErrorLabel(label string) bool {
	for _, l := range e.labels {
		if label == l {
			return true
		}
	}
	return false
}

func TestRejectionClassificationExcludesRetryAndUnknown(t *testing.T) {
	forbidden := errors.New("application forbidden")
	for _, tc := range []struct {
		name   string
		err    error
		reason RejectionReason
	}{
		{"invalid", eventlib.ErrInvalid, RejectionInvalid}, {"command-invalid", command.ErrInvalid, RejectionInvalid},
		{"conflict", eventlib.ErrConflict, RejectionConflict}, {"idempotency", eventlib.ErrIdempotency, RejectionIdempotency}, {"forbidden", forbidden, RejectionForbidden},
		{"cancel", errors.Join(eventlib.ErrInvalid, context.Canceled), ""}, {"deadline", errors.Join(eventlib.ErrConflict, context.DeadlineExceeded), ""},
		{"uncertain", errors.Join(eventlib.ErrIdempotency, eventlib.ErrUncertain), ""},
		{"network", errors.Join(eventlib.ErrInvalid, &net.OpError{Op: "write", Net: "tcp", Err: errors.New("broken pipe")}), ""},
		{"label", errors.Join(labeledDeliveryError{}, eventlib.ErrInvalid, labeledDeliveryError{labels: []string{"NetworkError"}}), ""},
		{"transient", errors.Join(forbidden, labeledDeliveryError{labels: []string{"TransientTransactionError"}}), ""},
		{"eof", errors.Join(eventlib.ErrInvalid, io.EOF), ""},
		{"reset", errors.Join(eventlib.ErrConflict, syscall.ECONNRESET), ""},
		{"untyped", errors.New("connection lost"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, j := journalFixture()
			body, _ := envelopeFixture(t)
			c := Consumer{Admit: admit, Rejections: j, Forbidden: func(err error) bool { return errors.Is(err, forbidden) }, Commands: appender(func(context.Context, []eventlib.Input) ([]eventlib.Receipt, error) { return nil, tc.err })}
			result, err := c.Consume(context.Background(), body)
			if tc.reason == "" {
				if !errors.Is(err, tc.err) || result.Rejected != nil || len(b.rows) != 0 {
					t.Fatal(result, err, b.rows)
				}
			} else if err != nil || result.Rejected == nil || result.Rejected.Reason != tc.reason || len(b.rows) != 1 {
				t.Fatal(result, err, b.rows)
			}
		})
	}
}

func TestRejectedAdmissionHTTPHeadersAndAuthentication(t *testing.T) {
	b, j := journalFixture()
	body, e := envelopeFixture(t)
	calls := 0
	c := Consumer{Admit: func(context.Context, Envelope) error { return command.ErrForbidden }, Forbidden: func(err error) bool { return errors.Is(err, command.ErrForbidden) }, Rejections: j, Commands: appender(func(context.Context, []eventlib.Input) ([]eventlib.Receipt, error) { calls++; return nil, nil })}
	h, err := c.Handler(func(r *http.Request) error {
		if r.Header.Get("Authorization") != "Bearer trusted" {
			return command.ErrForbidden
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/consume", bytes.NewReader(body))
		r.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := request(""); w.Code != 401 || len(b.rows) != 0 {
		t.Fatal(w.Code, b.rows)
	}
	for i := 0; i < 2; i++ {
		w := request("Bearer trusted")
		if w.Code != 204 || w.Header().Get("X-Blossm-Rejection") != e.ID || w.Header().Get("X-Blossm-Rejection-Reason") != "forbidden" || w.Body.Len() != 0 {
			t.Fatal(w.Code, w.Header(), w.Body.String())
		}
	}
	if calls != 0 || len(b.rows) != 1 {
		t.Fatal(calls, b.rows)
	}
	// Later permission changes cannot resurrect the exact rejected delivery.
	c.Admit = admit
	result, err := c.Consume(context.Background(), body)
	if err != nil || result.Rejected == nil || calls != 0 {
		t.Fatal(result, err, calls)
	}
}

func TestSQSPoisonRecordedBeforeAckThenValidContinues(t *testing.T) {
	b, j := journalFixture()
	body, e := envelopeFixture(t)
	valid := submission()
	valid.ID = "following"
	valid.Events[0].Key = "next"
	next, err := commandBody(valid)
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	c := Consumer{Admit: admit, Rejections: j, Commands: appender(func(_ context.Context, in []eventlib.Input) ([]eventlib.Receipt, error) {
		steps = append(steps, in[0].Key)
		if in[0].Key == "event" {
			return nil, eventlib.ErrIdempotency
		}
		return receipts(in, eventlib.Committed), nil
	})}
	queue := cloudaws.SQS{Client: sqsAPI{receive: func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
		return &sqs.ReceiveMessageOutput{Messages: []types.Message{{Body: sdk.String(string(body)), ReceiptHandle: sdk.String("poison")}, {Body: sdk.String(string(next)), ReceiptHandle: sdk.String("following")}}}, nil
	}, delete: func(ctx context.Context, in *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
		if *in.ReceiptHandle == "poison" {
			if _, err := j.Lookup(ctx, e.ID); err != nil {
				t.Fatal("ack before durable record", err)
			}
		}
		steps = append(steps, "ack:"+*in.ReceiptHandle)
		return &sqs.DeleteMessageOutput{}, nil
	}}}
	n, err := (SQSWorker{Queue: queue, Consumer: &c}).Poll(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: sdk.String("queue.fifo")})
	if err != nil || n != 2 || len(b.rows) != 1 || !reflect.DeepEqual(steps, []string{"event", "ack:poison", "next", "ack:following"}) {
		t.Fatal(n, err, steps, b.rows)
	}
}

func TestJournalUncertainStopsSQSWithoutAck(t *testing.T) {
	b, j := journalFixture()
	b.commitErr = eventlib.ErrUncertain
	b.commitDespiteError = true
	body, _ := envelopeFixture(t)
	acks := 0
	calls := 0
	c := Consumer{Admit: admit, Rejections: j, Commands: appender(func(context.Context, []eventlib.Input) ([]eventlib.Receipt, error) {
		calls++
		return nil, eventlib.ErrConflict
	})}
	queue := cloudaws.SQS{Client: sqsAPI{receive: func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
		return &sqs.ReceiveMessageOutput{Messages: []types.Message{{Body: sdk.String(string(body)), ReceiptHandle: sdk.String("one")}, {Body: sdk.String(string(body)), ReceiptHandle: sdk.String("two")}}}, nil
	}, delete: func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
		acks++
		return &sqs.DeleteMessageOutput{}, nil
	}}}
	n, err := (SQSWorker{Queue: queue, Consumer: &c}).Poll(context.Background(), &sqs.ReceiveMessageInput{QueueUrl: sdk.String("queue.fifo")})
	if n != 0 || acks != 0 || calls != 1 || !errors.Is(err, eventlib.ErrUncertain) {
		t.Fatal(n, err, acks, calls)
	}
}

func TestJournalConcurrentExactDuplicate(t *testing.T) {
	b, j := journalFixture()
	_, e := envelopeFixture(t)
	record := Rejection{EnvelopeID: e.ID, Reason: RejectionInvalid}
	done := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			got, _, err := j.Record(context.Background(), record)
			if err == nil && got != record {
				err = ErrRejectionMismatch
			}
			done <- err
		}()
	}
	for i := 0; i < 8; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(b.rows) != 1 {
		t.Fatal("duplicate records", b.rows)
	}
}
