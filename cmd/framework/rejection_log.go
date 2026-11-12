package main

import (
	"context"
	"io"
	"log/slog"

	"github.com/mejango/blossm-toolbox-go/delivery"
)

func runtimeLogger(output io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, nil))
}

// Delivery calls this only after confirming a fresh terminal record. No payload,
// claims or provider error text enters the log; the envelope ID enables lookup.
func rejectionLogger(logger *slog.Logger) func(context.Context, delivery.Rejection) error {
	return func(ctx context.Context, record delivery.Rejection) error {
		logger.WarnContext(ctx, "delivery rejected", "envelopeId", record.EnvelopeID, "reason", string(record.Reason))
		return nil
	}
}
