//go:build integration

package postgres

import (
	"os"
	"strings"
	"testing"

	"blossm.local/backend/eventstore"
	"blossm.local/backend/storage/internal/frontiertest"
)

func TestNativeFairFrontier(t *testing.T) {
	b, ctx := database(t)
	frontiertest.Fair(t, b, ctx, func() eventstore.Backend {
		next, err := Open(ctx, os.Getenv("POSTGRES_URL"), strings.Trim(b.schema, `"`))
		check(t, err)
		t.Cleanup(func() { next.Close(ctx) })
		return next
	})
}

func TestNativeFrontierCredit(t *testing.T) {
	b, ctx := database(t)
	frontiertest.CreditOnlyCurrent(t, b, ctx)
}
