package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	querymatch "github.com/mejango/blossm-toolbox-go/lib/query"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const cleanupTimeout = 5 * time.Second

type transaction struct {
	reader
	snapshotWrites map[string]int64
}

var _ eventlib.Tx = (*transaction)(nil)

func (t *transaction) insert(ctx context.Context, name string, value any, metadata bson.D) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	// JSON remains opaque binary, inline or split without changing a byte.
	doc, err := t.writeBody(ctx, body)
	if err != nil {
		return err
	}
	_, err = t.backend.collection(name).InsertOne(ctx, append(metadata, bodyFields(doc)...))
	return storageError(err)
}
func (t *transaction) PutEvent(ctx context.Context, event eventlib.Event) error {
	return t.insert(ctx, "events", event, bson.D{{Key: "_id", Value: event.Input.Key}, {Key: "hash", Value: event.Hash}, {Key: "root", Value: event.Input.Root.Key()}, {Key: "number", Value: event.Number}})
}
func (t *transaction) PutSnapshot(ctx context.Context, snapshot eventlib.Snapshot) error {
	terms, err := querymatch.State(snapshot.State)
	if err != nil {
		return err
	}
	if err = t.insert(ctx, "snapshots", snapshot, bson.D{{Key: "_id", Value: snapshot.Hash}, {Key: "root", Value: snapshot.Root.Key()}, {Key: "number", Value: snapshot.Number}}); err != nil {
		return err
	}
	if err = t.putHead(ctx, snapshot, terms); err != nil {
		return err
	}
	if t.snapshotWrites == nil {
		t.snapshotWrites = map[string]int64{}
	}
	t.snapshotWrites[snapshot.Root.Key()] = max(t.snapshotWrites[snapshot.Root.Key()], snapshot.Number)
	return nil
}
func (t *transaction) PutBlock(ctx context.Context, block eventlib.Block) error {
	if err := t.insert(ctx, "blocks", block, bson.D{{Key: "_id", Value: block.Number}, {Key: "hash", Value: block.Hash}}); err != nil {
		return err
	}
	return t.creditSnapshots(ctx, block.Number)
}
func (t *transaction) PutPublication(ctx context.Context, p eventlib.Publication) error {
	return t.insert(ctx, "publications", p, bson.D{{Key: "_id", Value: p.ID}, {Key: "attempts", Value: p.Attempts}})
}

// Incrementing a lock field forces a write conflict even when the count has not
// changed. MongoDB aborts contenders; the bounded outer loop takes a new snapshot.
func (t *transaction) LockRoots(ctx context.Context, roots []eventlib.Root) error {
	ordered := append([]eventlib.Root(nil), roots...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key() < ordered[j].Key() })
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	previous := ""
	for _, root := range ordered {
		key := root.Key()
		if key == previous {
			continue
		}
		previous = key
		body, err := json.Marshal(root)
		if err != nil {
			return err
		}
		_, err = t.backend.collection("counts").UpdateOne(ctx, id(key), bson.D{
			{Key: "$inc", Value: bson.D{{Key: "lock", Value: 1}}},
			{Key: "$setOnInsert", Value: append(scopeOf(root), bson.E{Key: "value", Value: int64(0)}, bson.E{Key: "body", Value: body})},
		}, options.UpdateOne().SetUpsert(true))
		// Concurrent first-use upserts can report a duplicate rather than a write conflict.
		if driver.IsDuplicateKeyError(err) {
			return errors.Join(retryTransaction, err)
		}
		if err != nil {
			return storageError(err)
		}
	}
	return nil
}
func (t *transaction) PutCount(ctx context.Context, root eventlib.Root, count int64) error {
	if count < 0 {
		return errors.New("mongo: negative count")
	}
	body, err := json.Marshal(root)
	if err != nil {
		return err
	}
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	_, err = t.backend.collection("counts").UpdateOne(ctx, id(root.Key()), bson.D{{Key: "$set", Value: append(scopeOf(root), bson.E{Key: "value", Value: count}, bson.E{Key: "body", Value: body})}}, options.UpdateOne().SetUpsert(true))
	return storageError(err)
}
func (t *transaction) DeletePublication(ctx context.Context, key string) error {
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	var doc document
	err = t.backend.collection("publications").FindOne(ctx, id(key)).Decode(&doc)
	if errors.Is(err, driver.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return storageError(err)
	}
	if _, err = t.readBody(ctx, doc); err != nil {
		return err
	}
	result, err := t.backend.collection("publications").DeleteOne(ctx, id(key))
	if err != nil {
		return storageError(err)
	}
	if result.DeletedCount != 1 {
		return retryTransaction
	}
	return t.deleteBody(ctx, doc)
}
func (t *transaction) FailPublication(ctx context.Context, key string) error {
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	result, err := t.backend.collection("publications").UpdateOne(ctx, id(key), bson.D{{Key: "$inc", Value: bson.D{{Key: "attempts", Value: 1}}}})
	if err != nil {
		return storageError(err)
	}
	if result.MatchedCount == 0 {
		return eventlib.ErrNotFound
	}
	return nil
}

// Read/compare and conditional replacement share the transaction snapshot. A
// concurrent insert/update aborts this transaction, so stale writers cannot win.
func (t *transaction) PutProjection(ctx context.Context, p eventlib.Projection) (bool, error) {
	existing, err := t.Projection(ctx, p.ID)
	if errors.Is(err, eventlib.ErrNotFound) {
		err = t.insert(ctx, "projections", p, append(scopeOf(p.Root), bson.E{Key: "_id", Value: p.ID}, bson.E{Key: "version", Value: p.Version}, bson.E{Key: "deleted", Value: p.Deleted}))
		if driver.IsDuplicateKeyError(err) {
			err = errors.Join(retryTransaction, err)
		}
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if existing.Root != p.Root {
		return false, eventlib.ErrConflict
	}
	if existing.Version >= p.Version {
		return false, nil
	}
	body, err := json.Marshal(p)
	if err != nil {
		return false, err
	}
	ctx, done, err := t.bind(ctx)
	if err != nil {
		return false, err
	}
	defer done()
	var previous document
	if err = t.backend.collection("projections").FindOne(ctx, id(p.ID)).Decode(&previous); err != nil {
		return false, storageError(err)
	}
	doc, err := t.writeBody(ctx, body)
	if err != nil {
		return false, err
	}
	filter := append(id(p.ID), bson.E{Key: "version", Value: existing.Version})
	fields := append(bodyFields(doc), bson.E{Key: "version", Value: p.Version}, bson.E{Key: "deleted", Value: p.Deleted})
	result, err := t.backend.collection("projections").UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: fields}})
	if err != nil {
		return false, storageError(err)
	}
	if result.MatchedCount != 1 {
		return false, retryTransaction
	}
	if err = t.deleteBody(ctx, previous); err != nil {
		return false, err
	}
	return true, nil
}
