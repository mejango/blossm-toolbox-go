//go:build integration

package mongo

import (
	"bytes"
	"encoding/json"
	"testing"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	querymatch "github.com/mejango/blossm-toolbox-go/lib/query"
	"github.com/mejango/blossm-toolbox-go/tests/querytest"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestNativeCandidates(t *testing.T)      { b, ctx := fixture(t); querytest.Candidates(t, b, ctx) }
func TestNativeCandidateReplay(t *testing.T) { b, ctx := fixture(t); querytest.Replay(t, b, ctx) }
func TestNativeProjectionListing(t *testing.T) {
	b, ctx := fixture(t)
	querytest.Projections(t, b, ctx)
}

func TestNativeCandidateIndex(t *testing.T) {
	b, ctx := fixture(t)
	querytest.Candidates(t, b, ctx)
	terms, err := querymatch.Predicates([]eventlib.Predicate{{Path: "n", Value: json.RawMessage(`2`)}})
	must(t, err)
	filter := append(scope("candidates", "service", "domain"), bson.E{Key: "state_terms", Value: bson.M{"$all": terms}})
	var plan bson.Raw
	must(t, b.db.RunCommand(ctx, bson.D{{Key: "explain", Value: bson.D{{Key: "find", Value: b.prefix + "snapshot_heads"}, {Key: "filter", Value: filter}, {Key: "projection", Value: bson.M{"_id": 1}}, {Key: "hint", Value: "network_1_service_1_domain_1_state_terms_1"}}}, {Key: "verbosity", Value: "queryPlanner"}}).Decode(&plan))
	if !bytes.Contains(plan, []byte("IXSCAN")) {
		t.Fatal("typed terms did not use the native multikey index")
	}
}
