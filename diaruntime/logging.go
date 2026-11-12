package diaruntime

import (
	"context"
	"errors"
	"log/slog"
)

// Provider errors may contain URL query credentials or response bodies. Keep
// their causes available to callers without serializing them to terminal logs.
type runtimeFailure struct {
	stage string
	cause error
}

func (e *runtimeFailure) Error() string { return e.stage + " failed" }
func (e *runtimeFailure) Unwrap() error { return e.cause }

// LogStopped reports a controlled lifecycle stage without provider credentials.
func LogStopped(logger *slog.Logger, err error) {
	stage := "runtime"
	var failure *runtimeFailure
	if errors.As(err, &failure) {
		stage = failure.stage
	}
	logger.Error("dia stopped", "stage", stage, "cancelled", errors.Is(err, context.Canceled), "deadline", errors.Is(err, context.DeadlineExceeded))
}
