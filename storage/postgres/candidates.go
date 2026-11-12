package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"blossm.local/backend/eventstore"
	"blossm.local/backend/storage/internal/queryindex"
)

var _ eventstore.CandidateReader = (*transaction)(nil)

// Candidates uses latest-head GIN terms for clean roots and a separate counter
// frontier for dirty roots. UNION keeps identities unique, with no row limit.
func (t *transaction) Candidates(ctx context.Context, network, service, domain string, predicates []eventstore.Predicate) ([]eventstore.Root, error) {
	terms, err := queryindex.Predicates(predicates)
	if err != nil {
		return nil, err
	}
	query := "SELECT root FROM " + t.schema + ".roots WHERE network=$1 AND service=$2 AND domain=$3 ORDER BY root"
	args := []any{network, service, domain}
	if len(terms) > 0 {
		query = "SELECT h.root FROM " + t.schema + ".snapshot_heads h JOIN " + t.schema + `.roots r ON r.root=h.root
   WHERE h.network=$1 AND h.service=$2 AND h.domain=$3 AND h.state_terms @> $4::text[] AND r.count<=h.last_event+1
   UNION SELECT r.root FROM ` + t.schema + ".roots r LEFT JOIN " + t.schema + `.snapshot_heads h ON h.root=r.root
   WHERE r.network=$1 AND r.service=$2 AND r.domain=$3 AND r.count>COALESCE(h.last_event,-1)+1 ORDER BY root`
		args = append(args, terms)
	}
	rows, err := t.tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]eventstore.Root, 0)
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var key string
		if err = rows.Scan(&key); err != nil {
			return nil, err
		}
		var root eventstore.Root
		if err = json.Unmarshal([]byte(key), &root); err != nil {
			return nil, err
		}
		result = append(result, root)
	}
	return result, rows.Err()
}

// Projections implements the optional listing seam owned by the view consumer.
func (t *transaction) Projections(ctx context.Context, network, service, domain, afterID string, limit int) ([]eventstore.Projection, error) {
	if limit < 1 {
		return nil, errors.New("projection limit must be positive")
	}
	rows, err := t.tx.Query(ctx, "SELECT data FROM "+t.schema+`.projections WHERE network=$1 AND service=$2 AND domain=$3 AND NOT deleted AND id>$4 ORDER BY id LIMIT $5`, network, service, domain, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]eventstore.Projection, 0)
	for rows.Next() {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var data []byte
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		var p eventstore.Projection
		if err = json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}
