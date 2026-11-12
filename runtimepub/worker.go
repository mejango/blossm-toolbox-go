// Package runtimepub owns publication worker lifecycle; providers own acknowledgment semantics.
package runtimepub

import (
	"context"
	"log/slog"
	"time"

	"blossm.local/backend/eventstore"
)

// Recover is best-effort at startup and after commitment; publication failures
// leave notices pending and never turn a committed command into a failed write.
func Recover(ctx context.Context, store *eventstore.Store, p eventstore.Publisher, batch int, logger *slog.Logger) {
	if err := store.Deliver(ctx, p, batch); err != nil {
		logger.Error("publication recovery failed", "errorType", "delivery", "pending", true)
	}
}
func Run(ctx context.Context, store *eventstore.Store, p eventstore.Publisher, batch int, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attempt, cancel := context.WithTimeout(ctx, 30*time.Second)
			Recover(attempt, store, p, batch, logger)
			cancel()
		}
	}
}
