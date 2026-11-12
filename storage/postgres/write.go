package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/storage/internal/queryindex"
)

func (t *transaction) LockRoots(ctx context.Context, roots []eventstore.Root) error {
	keys := make([]string, len(roots))
	for i, r := range roots {
		keys[i] = r.Key()
	}
	sort.Strings(keys)
	for i, key := range keys {
		if i > 0 && key == keys[i-1] {
			continue
		}
		if key < t.lastRoot {
			return errors.New("roots must be locked in one globally sorted order")
		}
		if _, err := t.tx.Exec(ctx, "INSERT INTO "+t.schema+".roots(root) VALUES ($1) ON CONFLICT DO NOTHING", key); err != nil {
			return err
		}
		if _, err := t.tx.Exec(ctx, "SELECT root FROM "+t.schema+".roots WHERE root=$1 FOR UPDATE", key); err != nil {
			return err
		}
		t.lastRoot = key
	}
	return nil
}
func (t *transaction) PutCount(ctx context.Context, root eventstore.Root, count int64) error {
	tag, err := t.tx.Exec(ctx, "UPDATE "+t.schema+".roots SET count=$2 WHERE root=$1", root.Key(), count)
	if err == nil && tag.RowsAffected() != 1 {
		return eventstore.ErrNotFound
	}
	return err
}
func (t *transaction) PutEvent(ctx context.Context, e eventstore.Event) error {
	return t.insert(ctx, "events(key,hash,root,number,data)", []any{e.Input.Key, e.Hash, e.Input.Root.Key(), e.Number}, e)
}
func (t *transaction) PutPublication(ctx context.Context, p eventstore.Publication) error {
	return t.insert(ctx, "publications(id,attempts,data)", []any{p.ID, p.Attempts}, p)
}
func (t *transaction) DeletePublication(ctx context.Context, id string) error {
	_, err := t.tx.Exec(ctx, "DELETE FROM "+t.schema+".publications WHERE id=$1", id)
	return err
}
func (t *transaction) FailPublication(ctx context.Context, id string) error {
	tag, err := t.tx.Exec(ctx, "UPDATE "+t.schema+".publications SET attempts=attempts+1 WHERE id=$1", id)
	if err == nil && tag.RowsAffected() == 0 {
		return eventstore.ErrNotFound
	}
	return err
}
func (t *transaction) PutSnapshot(ctx context.Context, s eventstore.Snapshot) error {
	terms, err := queryindex.State(s.State)
	if err != nil {
		return err
	}
	if err = t.insert(ctx, "snapshots(root,number,hash,data)", []any{s.Root.Key(), s.Number, s.Hash}, s); err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO "+t.schema+`.snapshot_heads AS current (root,number,last_event,state_terms) VALUES ($1,$2,$3,$4)
 ON CONFLICT (root) DO UPDATE SET number=EXCLUDED.number,last_event=EXCLUDED.last_event,state_terms=EXCLUDED.state_terms WHERE current.number<EXCLUDED.number`, s.Root.Key(), s.Number, s.LastEvent, terms)
	if err == nil {
		if t.snapshotWrites == nil {
			t.snapshotWrites = map[string]int64{}
		}
		t.snapshotWrites[s.Root.Key()] = max(t.snapshotWrites[s.Root.Key()], s.Number)
	}
	return err
}
func (t *transaction) PutBlock(ctx context.Context, b eventstore.Block) error {
	if err := t.insert(ctx, "blocks(number,hash,data)", []any{b.Number, b.Hash}, b); err != nil {
		return err
	}
	return t.creditSnapshots(ctx, b.Number)
}
func (t *transaction) PutProjection(ctx context.Context, p eventstore.Projection) (bool, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	tag, err := t.tx.Exec(ctx, "INSERT INTO "+t.schema+`.projections AS current (id,root,version,data,deleted) VALUES ($1,$2,$3,$4,$5)
 ON CONFLICT (id) DO UPDATE SET version=EXCLUDED.version,data=EXCLUDED.data,deleted=EXCLUDED.deleted
 WHERE current.root=EXCLUDED.root AND current.version<EXCLUDED.version`, p.ID, p.Root.Key(), p.Version, data, p.Deleted)
	if err != nil || tag.RowsAffected() == 1 {
		return tag.RowsAffected() == 1, err
	}
	existing, err := t.Projection(ctx, p.ID)
	if err != nil {
		return false, err
	}
	if existing.Root != p.Root {
		return false, eventstore.ErrConflict
	}
	return false, nil
}

// Each insert uses a constant number of bind parameters; arbitrarily large
// parent batches stay in one transaction without the 65,535-parameter ceiling.
func (t *transaction) insert(ctx context.Context, table string, args []any, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	args = append(args, data)
	placeholders := ""
	for i := range args {
		if i > 0 {
			placeholders += ","
		}
		placeholders += fmt.Sprintf("$%d", i+1)
	}
	_, err = t.tx.Exec(ctx, "INSERT INTO "+t.schema+"."+table+" VALUES ("+placeholders+")", args...)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return fmt.Errorf("%w: %w", eventstore.ErrConflict, err)
	}
	return err
}
