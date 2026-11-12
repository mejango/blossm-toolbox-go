package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	querymatch "github.com/mejango/blossm-toolbox-go/lib/query"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ eventlib.CandidateReader = (*reader)(nil)

func scope(network, service, domain string) bson.D {
	return bson.D{{Key: "network", Value: network}, {Key: "service", Value: service}, {Key: "domain", Value: domain}}
}
func scopeOf(root eventlib.Root) bson.D { return scope(root.Network, root.Service, root.Domain) }

func (t *transaction) putHead(ctx context.Context, s eventlib.Snapshot, terms []string) error {
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	unindexed := !boundedTerms(terms)
	if unindexed {
		terms = nil
	}
	head := append(scopeOf(s.Root), bson.E{Key: "_id", Value: s.Root.Key()}, bson.E{Key: "number", Value: s.Number}, bson.E{Key: "lastEvent", Value: s.LastEvent}, bson.E{Key: "state_terms", Value: terms}, bson.E{Key: "unindexed", Value: unindexed})
	newer := bson.D{{Key: "$gt", Value: bson.A{s.Number, bson.D{{Key: "$ifNull", Value: bson.A{"$number", int64(-1)}}}}}}
	replacement := bson.M{"$mergeObjects": bson.A{bson.M{"$literal": head}, bson.M{"lastSnapshotBlock": bson.M{"$ifNull": bson.A{"$lastSnapshotBlock", int64(-1)}}}}}
	update := driver.Pipeline{bson.D{{Key: "$replaceWith", Value: bson.D{{Key: "$cond", Value: bson.A{newer, replacement, "$$ROOT"}}}}}}
	_, err = t.backend.collection("snapshot_heads").UpdateOne(ctx, id(s.Root.Key()), update, options.UpdateOne().SetUpsert(true))
	if driver.IsDuplicateKeyError(err) {
		return errors.Join(retryTransaction, err)
	}
	return storageError(err)
}

// Candidates returns root identities only. Clean roots use the multikey scalar
// index; dirty roots are discovered by comparing counters to the latest head.
// The two reads share the caller's transaction snapshot and have no limit.
func (r *reader) Candidates(ctx context.Context, network, service, domain string, predicates []eventlib.Predicate) ([]eventlib.Root, error) {
	terms, err := querymatch.Predicates(predicates)
	if err != nil {
		return nil, err
	}
	ctx, done, err := r.bind(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	scoped := scope(network, service, domain)
	if len(terms) == 0 {
		return r.rootPipeline(ctx, "counts", driver.Pipeline{{{Key: "$match", Value: scoped}}, {{Key: "$sort", Value: bson.D{{Key: "_id", Value: 1}}}}})
	}
	cleanMatch := scope(network, service, domain)
	if boundedTerms(terms) {
		cleanMatch = append(cleanMatch, bson.E{Key: "$or", Value: bson.A{bson.M{"state_terms": bson.M{"$all": terms}}, bson.M{"unindexed": true}}})
	}
	count := bson.M{"$arrayElemAt": bson.A{"$count.value", 0}}
	frontier := bson.M{"$add": bson.A{"$lastEvent", 1}}
	clean := driver.Pipeline{
		{{Key: "$match", Value: cleanMatch}},
		lookup(r.backend.prefix+"counts", "count"),
		{{Key: "$match", Value: bson.M{"count.0": bson.M{"$exists": true}, "$expr": bson.M{"$lte": bson.A{count, frontier}}}}},
	}
	roots, err := r.rootPipeline(ctx, "snapshot_heads", clean)
	if err != nil {
		return nil, err
	}
	last := bson.M{"$ifNull": bson.A{bson.M{"$arrayElemAt": bson.A{"$head.lastEvent", 0}}, int64(-1)}}
	dirty := driver.Pipeline{
		{{Key: "$match", Value: scoped}}, lookup(r.backend.prefix+"snapshot_heads", "head"),
		{{Key: "$match", Value: bson.M{"$expr": bson.M{"$gt": bson.A{"$value", bson.M{"$add": bson.A{last, 1}}}}}}},
	}
	changed, err := r.rootPipeline(ctx, "counts", dirty)
	if err != nil {
		return nil, err
	}
	roots = append(roots, changed...)
	sort.Slice(roots, func(i, j int) bool { return roots[i].Key() < roots[j].Key() })
	return roots, nil
}
func lookup(from, as string) bson.D {
	return bson.D{{Key: "$lookup", Value: bson.D{{Key: "from", Value: from}, {Key: "localField", Value: "_id"}, {Key: "foreignField", Value: "_id"}, {Key: "as", Value: as}}}}
}
func (r *reader) rootPipeline(ctx context.Context, name string, pipeline driver.Pipeline) (roots []eventlib.Root, err error) {
	pipeline = append(pipeline, bson.D{{Key: "$project", Value: bson.D{{Key: "_id", Value: 1}}}})
	cursor, err := r.backend.collection(name).Aggregate(ctx, pipeline, options.Aggregate().SetBatchSize(128))
	if err != nil {
		return nil, storageError(err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		err = errors.Join(err, cursor.Close(c))
	}()
	roots = make([]eventlib.Root, 0)
	for cursor.Next(ctx) {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		var doc struct {
			Key string `bson:"_id"`
		}
		if err = cursor.Decode(&doc); err != nil {
			return nil, err
		}
		var root eventlib.Root
		if err = json.Unmarshal([]byte(doc.Key), &root); err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	return roots, storageError(cursor.Err())
}

// Projections is the structural reader seam consumed by view pagination.
func (r *reader) Projections(ctx context.Context, network, service, domain, afterID string, limit int) ([]eventlib.Projection, error) {
	if limit < 1 {
		return nil, errors.New("mongo: projection limit must be positive")
	}
	filter := append(scope(network, service, domain), bson.E{Key: "deleted", Value: false}, bson.E{Key: "_id", Value: bson.D{{Key: "$gt", Value: afterID}}})
	result := make([]eventlib.Projection, 0)
	err := r.scan(ctx, "projections", filter, bson.D{{Key: "_id", Value: 1}}, int64(limit), func(doc document) error {
		var p eventlib.Projection
		if err := json.Unmarshal(doc.Body, &p); err != nil {
			return err
		}
		result = append(result, p)
		return nil
	})
	return result, err
}

// Auxiliary metadata may broaden candidates, never truncate authoritative state.
// Keep head documents and multikey entries bounded; enormous query terms likewise
// fall back to scoped clean roots instead of exceeding the command BSON limit.
func boundedTerms(terms []string) bool {
	if len(terms) > 10000 {
		return false
	}
	remaining := 1 << 20
	for _, term := range terms {
		if len(term)+16 > remaining {
			return false
		}
		remaining -= len(term) + 16
	}
	return true
}
