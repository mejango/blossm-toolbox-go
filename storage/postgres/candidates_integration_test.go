//go:build integration

package postgres

import (
	"bytes"
	"encoding/json"
	"github.com/mejango/blossm-toolbox-go/storage/internal/queryindex"
	"github.com/mejango/blossm-toolbox-go/storage/internal/querytest"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"testing"
)

func TestNativeCandidates(t *testing.T)      { b, ctx := database(t); querytest.Candidates(t, b, ctx) }
func TestNativeCandidateReplay(t *testing.T) { b, ctx := database(t); querytest.Replay(t, b, ctx) }
func TestNativeProjectionListing(t *testing.T) {
	b, ctx := database(t)
	querytest.Projections(t, b, ctx)
}

func TestNativeCandidateIndex(t *testing.T) {
	b, ctx := database(t)
	querytest.Candidates(t, b, ctx)
	terms, err := queryindex.Predicates([]eventstore.Predicate{{Path: "n", Value: json.RawMessage(`2`)}})
	check(t, err)
	check(t, b.Update(ctx, func(tx eventstore.Tx) error {
		native := tx.(*transaction)
		if _, err := native.tx.Exec(ctx, "ANALYZE "+native.schema+".snapshot_heads"); err != nil {
			return err
		}
		if _, err := native.tx.Exec(ctx, "SET LOCAL enable_seqscan=off"); err != nil {
			return err
		}
		var plan []byte
		if err := native.tx.QueryRow(ctx, "EXPLAIN (FORMAT JSON) SELECT root FROM "+native.schema+".snapshot_heads WHERE network=$1 AND service=$2 AND domain=$3 AND state_terms @> $4::text[]", "candidates", "service", "domain", terms).Scan(&plan); err != nil {
			return err
		}
		if !bytes.Contains(plan, []byte("heads_terms")) {
			t.Fatalf("typed terms did not use native GIN index: %s", plan)
		}
		return nil
	}))
}
