package mongo

import (
	"context"
	"errors"
	"sort"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
)

func (r *reader) PendingRoots(ctx context.Context, limit int) ([]eventlib.Root, error) {
	if limit < 1 {
		return nil, errors.New("positive root limit required")
	}
	ctx, done, err := r.bind(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	last := bson.M{"$ifNull": bson.A{bson.M{"$arrayElemAt": bson.A{"$head.lastEvent", 0}}, int64(-1)}}
	served := bson.M{"$ifNull": bson.A{bson.M{"$arrayElemAt": bson.A{"$head.lastSnapshotBlock", 0}}, int64(-1)}}
	pipeline := driver.Pipeline{
		lookup(r.backend.prefix+"snapshot_heads", "head"),
		{{Key: "$match", Value: bson.M{"$expr": bson.M{"$gt": bson.A{"$value", bson.M{"$add": bson.A{last, 1}}}}}}},
		{{Key: "$set", Value: bson.M{"hasSnapshot": bson.M{"$gt": bson.A{bson.M{"$size": "$head"}, 0}}, "lastSnapshotBlock": served}}},
		{{Key: "$sort", Value: bson.D{{Key: "hasSnapshot", Value: 1}, {Key: "lastSnapshotBlock", Value: 1}, {Key: "_id", Value: 1}}}},
		{{Key: "$limit", Value: int64(limit)}},
	}
	return r.rootPipeline(ctx, "counts", pipeline)
}

// A standalone snapshot does not receive completed-block service credit.
func (t *transaction) creditSnapshots(ctx context.Context, block int64) error {
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	keys := make([]string, 0, len(t.snapshotWrites))
	for root := range t.snapshotWrites {
		keys = append(keys, root)
	}
	sort.Strings(keys)
	for _, root := range keys {
		filter := bson.D{{Key: "_id", Value: root}, {Key: "number", Value: t.snapshotWrites[root]}}
		if _, err = t.backend.collection("snapshot_heads").UpdateOne(ctx, filter, bson.D{{Key: "$max", Value: bson.D{{Key: "lastSnapshotBlock", Value: block}}}}); err != nil {
			return storageError(err)
		}
	}
	t.snapshotWrites = nil
	return nil
}
