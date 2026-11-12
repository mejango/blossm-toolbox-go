//go:build integration

package dia

import (
	"context"
	"errors"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"testing"
)

// Preserve transaction evidence on failure without changing retry semantics.
type diagnosticBackend struct {
	eventstore.Backend
	t *testing.T
}

func (b diagnosticBackend) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	attempts := 0
	err := b.Backend.Update(ctx, func(tx eventstore.Tx) error { attempts++; return fn(tx) })
	if err != nil {
		var labeled interface{ HasErrorLabel(string) bool }
		transient, uncertain := false, false
		if errors.As(err, &labeled) {
			transient = labeled.HasErrorLabel("TransientTransactionError")
			uncertain = labeled.HasErrorLabel("UnknownTransactionCommitResult")
		}
		b.t.Logf("transaction failed: callbacks=%d transient=%t uncertain=%t type=%T error=%v", attempts, transient, uncertain, err, err)
	}
	return err
}
