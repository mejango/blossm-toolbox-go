package postgres

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"blossm.local/backend/eventstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type commitError struct {
	pgx.Tx
	err error
}

func (t commitError) Commit(context.Context) error { return t.err }
func TestNamespace(t *testing.T) {
	for _, s := range []string{"", strings.Repeat("a", 64), strings.Repeat("é", 32), "x\x00y", string([]byte{255})} {
		if validateNamespace(s) == nil {
			t.Fatalf("accepted %q", s)
		}
	}
	for _, s := range []string{strings.Repeat("a", 63), strings.Repeat("é", 31) + "x", `quoted"; DROP SCHEMA public;--`} {
		if err := validateNamespace(s); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCommitClassification(t *testing.T) {
	for _, tc := range []struct {
		err              error
		retry, uncertain bool
	}{
		{io.ErrUnexpectedEOF, false, true}, {context.Canceled, false, true},
		{&pgconn.PgError{Code: "40001"}, true, false}, {&pgconn.PgError{Code: "40P01"}, true, false},
		{&pgconn.PgError{Code: "23505"}, false, false}, {pgx.ErrTxCommitRollback, false, false},
	} {
		err := commit(context.Background(), commitError{err: tc.err})
		if retryable(err) != tc.retry || errors.Is(err, eventstore.ErrUncertain) != tc.uncertain || !errors.Is(err, tc.err) {
			t.Fatalf("classification %v: %v", tc, err)
		}
	}
}

func TestRetryConfig(t *testing.T) {
	defaults, err := (RetryConfig{}).normalized()
	if err != nil || defaults.MaxAttempts != 10 || defaults.BaseDelay != 10*time.Millisecond || defaults.MaxDelay != 250*time.Millisecond {
		t.Fatal(defaults, err)
	}
	for _, c := range []RetryConfig{{MaxAttempts: -1}, {BaseDelay: -1}, {MaxDelay: -1}, {BaseDelay: time.Second, MaxDelay: time.Millisecond}} {
		if _, err := OpenWithRetry(context.Background(), "invalid", "test", c); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	explicit := RetryConfig{MaxAttempts: 1, BaseDelay: time.Nanosecond, MaxDelay: time.Second}
	if got, err := explicit.normalized(); err != nil || got != explicit {
		t.Fatal(got, err)
	}
}
func TestRetryDelayBounds(t *testing.T) {
	for _, ceiling := range []time.Duration{1, 10 * time.Millisecond, 250 * time.Millisecond, time.Duration(1<<63 - 1)} {
		for i := 0; i < 100; i++ {
			d := jitter(ceiling)
			if d < ceiling/2 || d > ceiling {
				t.Fatalf("delay %v exceeds half-to-full ceiling %v", d, ceiling)
			}
		}
	}
	for _, tc := range []struct{ current, max, want time.Duration }{{5, 20, 10}, {10, 20, 20}, {15, 20, 20}, {1 << 62, 1<<63 - 1, 1<<63 - 1}} {
		if got := nextDelay(tc.current, tc.max); got != tc.want {
			t.Fatalf("delay overflow/cap: %v -> %v", tc, got)
		}
	}
}
