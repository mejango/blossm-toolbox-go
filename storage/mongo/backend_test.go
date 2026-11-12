package mongo

import (
	"context"
	"errors"
	"testing"

	"github.com/mejango/blossm-toolbox-go/stores/event"
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
	if uncertain(nil) || retryable(eventstore.ErrConflict) {
		t.Fatal("success/conflict misclassified")
	}
	duplicate := driver.WriteException{WriteErrors: driver.WriteErrors{{Code: 11000}}}
	if !errors.Is(storageError(duplicate), eventstore.ErrConflict) {
		t.Fatal("duplicate is conflict")
	}
	if !errors.Is(storageError(driver.ErrNoDocuments), eventstore.ErrNotFound) {
		t.Fatal("missing is not found")
	}
}
func TestConfiguration(t *testing.T) {
	for _, cfg := range []Config{{}, {URI: "mongodb://localhost", Database: "db", Namespace: ""}, {URI: "mongodb://localhost", Database: "a.b", Namespace: "a"}, {URI: "mongodb://localhost", Database: "db", Namespace: "a", MaxAttempts: -1}} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted %#v", cfg)
		}
	}
}
