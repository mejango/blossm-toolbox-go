package mongo

import (
	"context"
	"errors"
	"testing"
	"time"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
)

func TestCommitClassification(t *testing.T) {
	unknown := driver.CommandError{Labels: []string{"UnknownTransactionCommitResult", "TransientTransactionError"}}
	if !uncertain(unknown) {
		t.Fatal("unknown commit must take precedence over transient")
	}
	transient := driver.CommandError{Labels: []string{"TransientTransactionError"}}
	if uncertain(transient) || !retryable(transient) {
		t.Fatal("known aborted transaction should be retryable")
	}
	if !uncertain(context.Canceled) || !uncertain(context.DeadlineExceeded) {
		t.Fatal("interrupted commit is uncertain")
	}
	if uncertain(nil) || retryable(eventlib.ErrConflict) {
		t.Fatal("success/conflict misclassified")
	}
	duplicate := driver.WriteException{WriteErrors: driver.WriteErrors{{Code: 11000}}}
	if !errors.Is(storageError(duplicate), eventlib.ErrConflict) {
		t.Fatal("duplicate is conflict")
	}
	if !errors.Is(storageError(driver.ErrNoDocuments), eventlib.ErrNotFound) {
		t.Fatal("missing is not found")
	}
}
func TestConfiguration(t *testing.T) {
	for _, cfg := range []Config{{}, {URI: "mongodb://localhost", Database: "db", Namespace: ""}, {URI: "mongodb://localhost", Database: "a.b", Namespace: "a"}, {URI: "mongodb://localhost", Database: "db", Namespace: "a", MaxAttempts: -1}, {URI: "mongodb://localhost", Database: "db", Namespace: "a", MaxAttempts: 101}} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
}

func TestRetryDelayBounds(t *testing.T) {
	for attempt, milliseconds := range []int{10, 20, 40, 80, 160, 250, 250, 250} {
		ceiling := time.Duration(milliseconds) * time.Millisecond
		samples := map[time.Duration]bool{}
		for i := 0; i < 100; i++ {
			delay := retryDelay(attempt)
			samples[delay] = true
			if delay < ceiling/2 || delay > ceiling {
				t.Fatalf("attempt %d delay %s outside [%s,%s]", attempt, delay, ceiling/2, ceiling)
			}
		}
		if len(samples) < 2 {
			t.Fatalf("attempt %d: jitter produced only one distinct delay in 100 samples", attempt)
		}
	}
	if delay := retryDelay(100); delay < 125*time.Millisecond || delay > 250*time.Millisecond {
		t.Fatal("cap lost", delay)
	}
}
func TestRetryWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitRetry(ctx, 99); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, stop := context.WithTimeout(context.Background(), time.Millisecond)
	defer stop()
	start := time.Now()
	if err := waitRetry(ctx, 99); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("wait ignored deadline", err)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("deadline cancellation took %s; want <100ms, before the retry timer minimum of125ms", elapsed)
	}
	if err := waitRetry(context.Background(), 0); err != nil {
		t.Fatal("completed wait", err)
	}
}
func TestRetryClassificationDoesNotExpand(t *testing.T) {
	for _, err := range []error{driver.CommandError{Code: 112, Message: "WriteConflict"}, errors.New("TransientTransactionError WriteConflict"), eventlib.ErrConflict} {
		if retryable(err) {
			t.Fatal("unlabeled error became retryable", err)
		}
	}
	if !retryable(retryTransaction) {
		t.Fatal("owned known abort lost")
	}
}
