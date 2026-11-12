//go:build integration

package mongo

import (
	"os"
	"testing"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/tests/frontiertest"
)

func TestNativeFairFrontier(t *testing.T) {
	b, ctx := fixture(t)
	frontiertest.Fair(t, b, ctx, func() eventlib.Backend {
		next, err := Open(ctx, Config{URI: os.Getenv("MONGODB_URI"), Database: b.db.Name(), Namespace: "test", MaxAttempts: 30})
		must(t, err)
		t.Cleanup(func() { must(t, next.Close(ctx)) })
		return next
	})
}

func TestNativeFrontierCredit(t *testing.T) {
	b, ctx := fixture(t)
	frontiertest.CreditOnlyCurrent(t, b, ctx)
}
