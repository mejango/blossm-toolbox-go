package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mejango/blossm-toolbox-go/delivery"
)

func TestRejectionLoggerEmitsOnlySafeRecord(t *testing.T) {
	var output bytes.Buffer
	logger := runtimeLogger(&output)
	callback := rejectionLogger(logger)
	record := delivery.Rejection{EnvelopeID: strings.Repeat("a", 64), Reason: delivery.RejectionForbidden}
	type privateContextKey struct{}
	ctx := context.WithValue(context.Background(), privateContextKey{}, "postgres://user:password@host/?token=SYNTHETIC_SECRET_CANARY")
	if err := callback(ctx, record); err != nil {
		t.Fatal(err)
	}
	if strings.Count(output.String(), "\n") != 1 || strings.Contains(output.String(), "SYNTHETIC_SECRET_CANARY") {
		t.Fatal("unexpected or sensitive log", output.String())
	}
	var fields map[string]any
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 5 || fields["level"] != "WARN" || fields["msg"] != "delivery rejected" || fields["envelopeId"] != record.EnvelopeID || fields["reason"] != string(record.Reason) {
		t.Fatal("incorrect rejection log fields", fields)
	}
}
