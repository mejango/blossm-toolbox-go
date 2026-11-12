// Package postgres implements the eventstore protocol using isolated PostgreSQL schemas.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
	"unicode/utf8"

	"blossm.local/backend/eventstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RetryConfig bounds Update retries. MaxAttempts includes the first attempt;
// one disables retries. Zero fields select defaults: 10 attempts, 10ms initial
// delay ceiling and 250ms maximum ceiling. Negative fields are invalid.
// Every wait is independently jittered between half its ceiling and the ceiling.
// Context deadlines can terminate the transaction or wait earlier.
type RetryConfig struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func (c RetryConfig) normalized() (RetryConfig, error) {
	if c.MaxAttempts < 0 || c.BaseDelay < 0 || c.MaxDelay < 0 {
		return c, errors.New("retry settings must not be negative")
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 10
	}
	if c.BaseDelay == 0 {
		c.BaseDelay = 10 * time.Millisecond
	}
	if c.MaxDelay == 0 {
		c.MaxDelay = 250 * time.Millisecond
	}
	if c.BaseDelay > c.MaxDelay {
		return c, errors.New("retry base delay exceeds maximum delay")
	}
	return c, nil
}
func jitter(ceiling time.Duration) time.Duration {
	half := ceiling / 2
	return half + time.Duration(rand.Int64N(int64(ceiling-half)+1))
}
func nextDelay(current, maximum time.Duration) time.Duration {
	if current >= maximum-current {
		return maximum
	}
	return current * 2
}

// Store owns its connection pool. Callbacks must not retain Reader/Tx or perform
// external side effects: Update can repeat a callback after a known abort.
type Store struct {
	pool   *pgxpool.Pool
	schema string
	retry  RetryConfig
}

var _ eventstore.Backend = (*Store)(nil)

// Open waits for atomic schema initialization before returning. Namespace is an
// explicit PostgreSQL identifier, never a search_path or silently truncated name.
func Open(ctx context.Context, url, namespace string) (*Store, error) {
	return OpenWithRetry(ctx, url, namespace, RetryConfig{})
}

// OpenWithRetry opens a store with a bounded transaction retry policy.
func OpenWithRetry(ctx context.Context, url, namespace string, config RetryConfig) (*Store, error) {
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, schema: pgx.Identifier{namespace}.Sanitize(), retry: config}
	if err = s.initialize(ctx, namespace); err != nil {
		pool.Close()
		return nil, err
	}
	return s, nil
}
func validateNamespace(s string) error {
	if s == "" || len(s) > 63 || !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return errors.New("postgres namespace must contain 1 to 63 valid UTF-8 bytes without NUL")
	}
	return nil
}
func (s *Store) Close(context.Context) error { s.pool.Close(); return nil }
func (s *Store) View(ctx context.Context, fn func(eventstore.Reader) error) error {
	return s.once(ctx, pgx.ReadOnly, func(t *transaction) error { return fn(t) })
}
func (s *Store) Update(ctx context.Context, fn func(eventstore.Tx) error) error {
	ceiling := s.retry.BaseDelay
	for attempt := 1; ; attempt++ {
		err := s.once(ctx, pgx.ReadWrite, func(t *transaction) error { return fn(t) })
		if !retryable(err) || attempt >= s.retry.MaxAttempts {
			return err
		}
		timer := time.NewTimer(jitter(ceiling))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		ceiling = nextDelay(ceiling, s.retry.MaxDelay)
	}
}
func (s *Store) once(ctx context.Context, mode pgx.TxAccessMode, fn func(*transaction) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: mode})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = fn(&transaction{tx: tx, schema: s.schema}); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return commit(ctx, tx)
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func retryable(err error) bool {
	if errors.Is(err, eventstore.ErrUncertain) {
		return false
	}
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40001" || pg.Code == "40P01")
}

// A server error reports a known rejected COMMIT; a transport error cannot tell
// us whether COMMIT reached the server. Never replay that callback automatically.
func commit(ctx context.Context, tx pgx.Tx) error {
	err := tx.Commit(ctx)
	if err == nil {
		return nil
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) || errors.Is(err, pgx.ErrTxCommitRollback) {
		return err
	}
	return fmt.Errorf("%w: %w", eventstore.ErrUncertain, err)
}
func (s *Store) initialize(ctx context.Context, namespace string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "blossm-schema:"+namespace); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+s.schema); err != nil {
		return err
	}
	for _, table := range tables {
		if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+s.schema+"."+strings.ReplaceAll(table, "%ROOT%", s.schema+".roots(root)")); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "CREATE INDEX IF NOT EXISTS publications_pending ON "+s.schema+".publications (attempts,id)"); err != nil {
		return err
	}
	for _, index := range []string{
		"roots_scope ON " + s.schema + ".roots (network,service,domain,root)",
		"heads_scope ON " + s.schema + ".snapshot_heads (network,service,domain,root)",
		"heads_terms ON " + s.schema + ".snapshot_heads USING gin (state_terms)",
		"projections_scope ON " + s.schema + ".projections (network,service,domain,id) WHERE NOT deleted",
	} {
		if _, err = tx.Exec(ctx, "CREATE INDEX IF NOT EXISTS "+index); err != nil {
			return err
		}
	}
	return commit(ctx, tx)
}

var tables = []string{
	`roots (root text COLLATE "C" PRIMARY KEY, count bigint NOT NULL DEFAULT 0 CHECK (count >= 0), network text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'network') STORED, service text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'service') STORED, domain text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'domain') STORED)`,
	`events (key text PRIMARY KEY, hash text NOT NULL UNIQUE, root text COLLATE "C" NOT NULL REFERENCES %ROOT%, number bigint NOT NULL CHECK (number >= 0), data bytea NOT NULL, UNIQUE (root, number))`,
	`publications (id text COLLATE "C" PRIMARY KEY, attempts bigint NOT NULL CHECK (attempts >= 0), data bytea NOT NULL)`,
	`snapshots (root text COLLATE "C" NOT NULL, number bigint NOT NULL CHECK (number >= 0), hash text NOT NULL UNIQUE, data bytea NOT NULL, PRIMARY KEY (root, number))`,
	`blocks (number bigint PRIMARY KEY CHECK (number >= 0), hash text NOT NULL UNIQUE, data bytea NOT NULL)`,
	`projections (id text COLLATE "C" PRIMARY KEY, root text COLLATE "C" NOT NULL, version bigint NOT NULL CHECK (version >= 0), data bytea NOT NULL, deleted boolean NOT NULL,
 network text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'network') STORED,
 service text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'service') STORED,
 domain text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'domain') STORED)`,
	`snapshot_heads (root text COLLATE "C" PRIMARY KEY, number bigint NOT NULL, last_event bigint NOT NULL, last_snapshot_block bigint NOT NULL DEFAULT -1, state_terms text[] NOT NULL,
 network text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'network') STORED,
 service text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'service') STORED,
 domain text COLLATE "C" GENERATED ALWAYS AS (root::jsonb->>'domain') STORED)`,
}
