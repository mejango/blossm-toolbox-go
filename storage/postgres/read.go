package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/mejango/blossm-toolbox-go/eventstore"
)

type transaction struct {
	tx             pgx.Tx
	schema         string
	cursor         uint64
	lastRoot       string
	snapshotWrites map[string]int64
}

var _ eventstore.Tx = (*transaction)(nil)

func read[T any](ctx context.Context, t *transaction, query string, args ...any) (T, error) {
	var value T
	var data []byte
	err := t.tx.QueryRow(ctx, query, args...).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, eventstore.ErrNotFound
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	return value, err
}
func (t *transaction) Find(ctx context.Context, key string) (eventstore.Event, error) {
	return read[eventstore.Event](ctx, t, "SELECT data FROM "+t.schema+".events WHERE key=$1", key)
}
func (t *transaction) Count(ctx context.Context, root eventstore.Root) (int64, error) {
	var count int64
	err := t.tx.QueryRow(ctx, "SELECT count FROM "+t.schema+".roots WHERE root=$1", root.Key()).Scan(&count)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return count, err
}
func (t *transaction) Snapshot(ctx context.Context, root eventstore.Root) (eventstore.Snapshot, error) {
	return read[eventstore.Snapshot](ctx, t, "SELECT data FROM "+t.schema+".snapshots WHERE root=$1 ORDER BY number DESC LIMIT 1", root.Key())
}
func (t *transaction) LastBlock(ctx context.Context) (eventstore.Block, error) {
	return read[eventstore.Block](ctx, t, "SELECT data FROM "+t.schema+".blocks ORDER BY number DESC LIMIT 1")
}
func (t *transaction) Projection(ctx context.Context, id string) (eventstore.Projection, error) {
	return read[eventstore.Projection](ctx, t, "SELECT data FROM "+t.schema+".projections WHERE id=$1", id)
}
func (t *transaction) Pending(ctx context.Context, limit int) ([]eventstore.Publication, error) {
	if limit <= 0 {
		return nil, errors.New("pending limit must be positive")
	}
	rows, err := t.tx.Query(ctx, "SELECT data, attempts FROM "+t.schema+".publications ORDER BY attempts, id LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]eventstore.Publication, 0)
	for rows.Next() {
		var p eventstore.Publication
		var data []byte
		if err = rows.Scan(&data, &p.Attempts); err != nil {
			return nil, err
		}
		attempts := p.Attempts
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		p.Attempts = attempts
		out = append(out, p)
	}
	return out, rows.Err()
}
func (t *transaction) Events(ctx context.Context, root eventstore.Root, after int64, fn func(eventstore.Event) error) error {
	return t.stream(ctx, "SELECT data FROM "+t.schema+".events WHERE root=$1 AND number>$2 ORDER BY number", []any{root.Key(), after}, func(data []byte) error {
		var e eventstore.Event
		if err := json.Unmarshal(data, &e); err != nil {
			return err
		}
		return fn(e)
	})
}
func (t *transaction) Roots(ctx context.Context, fn func(eventstore.Root) error) error {
	return t.stream(ctx, "SELECT convert_to(root,'UTF8') FROM "+t.schema+".roots ORDER BY root", nil, func(data []byte) error {
		var r eventstore.Root
		if err := json.Unmarshal(data, &r); err != nil {
			return err
		}
		return fn(r)
	})
}

// Fetch and close each page before invoking callbacks, allowing nested reads and
// writes on the same transaction without pgx's connection-busy failure.
func (t *transaction) stream(ctx context.Context, query string, args []any, fn func([]byte) error) (err error) {
	t.cursor++
	name := pgx.Identifier{fmt.Sprintf("blossm_cursor_%d", t.cursor)}.Sanitize()
	if _, err = t.tx.Exec(ctx, "DECLARE "+name+" NO SCROLL CURSOR FOR "+query, args...); err != nil {
		return err
	}
	defer func() {
		_, closeErr := t.tx.Exec(ctx, "CLOSE "+name)
		if err == nil {
			err = closeErr
		}
	}()
	for {
		var rows pgx.Rows
		rows, err = t.tx.Query(ctx, "FETCH FORWARD 100 FROM "+name)
		if err != nil {
			return err
		}
		data, collectErr := pgx.CollectRows(rows, pgx.RowTo[[]byte])
		if collectErr != nil {
			return collectErr
		}
		for _, item := range data {
			if err = ctx.Err(); err != nil {
				return err
			}
			if err = fn(item); err != nil {
				return err
			}
		}
		if len(data) < 100 {
			return ctx.Err()
		}
	}
}
