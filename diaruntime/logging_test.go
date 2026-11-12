package diaruntime

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestStartupLogDoesNotExposeProviderCauses(t *testing.T) {
	for _, err := range []error{
		errors.New("postgres://user:password@localhost/db?token=SYNTHETIC_SECRET_CANARY"),
		&runtimeFailure{stage: "database", cause: errors.New("invalid proxyPassword SYNTHETIC_SECRET_CANARY")},
		&runtimeFailure{stage: "publication runtime", cause: errors.Join(context.DeadlineExceeded, errors.New("AWS_SESSION_TOKEN=SYNTHETIC_SECRET_CANARY"))},
	} {
		var output bytes.Buffer
		LogStopped(slog.New(slog.NewJSONHandler(&output, nil)), err)
		if strings.Contains(output.String(), "SYNTHETIC_SECRET_CANARY") || strings.Contains(output.String(), "password") {
			t.Fatal("credential cause logged")
		}
		if !strings.Contains(output.String(), `"stage"`) {
			t.Fatal("stage missing")
		}
	}
	cause := errors.New("provider cause")
	err := &runtimeFailure{stage: "database", cause: cause}
	if !errors.Is(err, cause) {
		t.Fatal("cause chain lost")
	}
}
