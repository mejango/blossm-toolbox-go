package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"sort"
)

func (t *transaction) PendingRoots(ctx context.Context, limit int) ([]eventstore.Root, error) {
	if limit < 1 {
		return nil, errors.New("positive root limit required")
	}
	rows, e := t.tx.Query(ctx, "SELECT r.root FROM "+t.schema+".roots r LEFT JOIN "+t.schema+".snapshot_heads h ON h.root=r.root WHERE r.count>COALESCE(h.last_event,-1)+1 ORDER BY (h.root IS NOT NULL), COALESCE(h.last_snapshot_block,-1), r.root LIMIT $1", limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []eventstore.Root{}
	for rows.Next() {
		var key string
		if e = rows.Scan(&key); e != nil {
			return nil, e
		}
		var r eventstore.Root
		if e = json.Unmarshal([]byte(key), &r); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Service credit changes only with a block and only for the snapshot head
// written by this transaction. Rollback restores both the data and its priority.
func (t *transaction) creditSnapshots(ctx context.Context, block int64) error {
	keys := make([]string, 0, len(t.snapshotWrites))
	for root := range t.snapshotWrites {
		keys = append(keys, root)
	}
	sort.Strings(keys)
	for _, root := range keys {
		if _, err := t.tx.Exec(ctx, "UPDATE "+t.schema+".snapshot_heads SET last_snapshot_block=$3 WHERE root=$1 AND number=$2 AND last_snapshot_block<$3", root, t.snapshotWrites[root], block); err != nil {
			return err
		}
	}
	t.snapshotWrites = nil
	return nil
}
