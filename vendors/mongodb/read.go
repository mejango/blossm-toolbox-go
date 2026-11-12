package mongo

import (
	"context"
	"encoding/json"
	"errors"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type reader struct {
	backend *Backend
	session *driver.Session
	ctx     context.Context
	active  bool
}

var _ eventlib.Reader = (*reader)(nil)

// Contexts supplied by callers retain cancellation while always using this
// reader's session. Readers and transactions are scoped to the callback and serial.
func (r *reader) bind(ctx context.Context) (context.Context, func(), error) {
	if !r.active {
		return nil, nil, errors.New("mongo: reader used outside callback")
	}
	if err := r.ctx.Err(); err != nil {
		return nil, nil, err
	}
	child, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	return driver.NewSessionContext(child, r.session), func() { stop(); cancel() }, nil
}

type document struct {
	Blob     *blobManifest `bson:"blob,omitempty"`
	Body     []byte        `bson:"body"`
	Attempts int64         `bson:"attempts,omitempty"`
}

func readOne[T any](r *reader, ctx context.Context, collection string, filter bson.D, sort bson.D) (value T, err error) {
	ctx, done, err := r.bind(ctx)
	if err != nil {
		return value, err
	}
	defer done()
	var doc document
	opts := options.FindOne()
	if len(sort) > 0 {
		opts.SetSort(sort)
	}
	if err = r.backend.collection(collection).FindOne(ctx, filter, opts).Decode(&doc); err != nil {
		return value, storageError(err)
	}
	body, err := r.readBody(ctx, doc)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(body, &value)
	return value, err
}
func id(value any) bson.D                  { return bson.D{{Key: "_id", Value: value}} }
func rootFilter(root eventlib.Root) bson.D { return bson.D{{Key: "root", Value: root.Key()}} }

func (r *reader) Find(ctx context.Context, key string) (eventlib.Event, error) {
	return readOne[eventlib.Event](r, ctx, "events", id(key), nil)
}
func (r *reader) Count(ctx context.Context, root eventlib.Root) (int64, error) {
	ctx, done, err := r.bind(ctx)
	if err != nil {
		return 0, err
	}
	defer done()
	var count struct {
		Value int64 `bson:"value"`
	}
	err = r.backend.collection("counts").FindOne(ctx, id(root.Key())).Decode(&count)
	if errors.Is(err, driver.ErrNoDocuments) {
		return 0, nil
	}
	return count.Value, storageError(err)
}
func (r *reader) Snapshot(ctx context.Context, root eventlib.Root) (eventlib.Snapshot, error) {
	return readOne[eventlib.Snapshot](r, ctx, "snapshots", rootFilter(root), bson.D{{Key: "number", Value: -1}})
}
func (r *reader) LastBlock(ctx context.Context) (eventlib.Block, error) {
	return readOne[eventlib.Block](r, ctx, "blocks", bson.D{}, bson.D{{Key: "_id", Value: -1}})
}
func (r *reader) Projection(ctx context.Context, key string) (eventlib.Projection, error) {
	return readOne[eventlib.Projection](r, ctx, "projections", id(key), nil)
}

func (r *reader) Events(ctx context.Context, root eventlib.Root, after int64, fn func(eventlib.Event) error) error {
	filter := append(rootFilter(root), bson.E{Key: "number", Value: bson.D{{Key: "$gt", Value: after}}})
	return r.scan(ctx, "events", filter, bson.D{{Key: "number", Value: 1}}, 0, func(doc document) error {
		var event eventlib.Event
		if err := json.Unmarshal(doc.Body, &event); err != nil {
			return err
		}
		return fn(event)
	})
}
func (r *reader) Roots(ctx context.Context, fn func(eventlib.Root) error) error {
	return r.scan(ctx, "counts", bson.D{}, bson.D{{Key: "_id", Value: 1}}, 0, func(doc document) error {
		var root eventlib.Root
		if err := json.Unmarshal(doc.Body, &root); err != nil {
			return err
		}
		return fn(root)
	})
}
func (r *reader) Pending(ctx context.Context, limit int) ([]eventlib.Publication, error) {
	if limit <= 0 {
		return nil, errors.New("mongo: pending limit must be positive")
	}
	result := make([]eventlib.Publication, 0)
	err := r.scan(ctx, "publications", bson.D{}, bson.D{{Key: "attempts", Value: 1}, {Key: "_id", Value: 1}}, int64(limit), func(doc document) error {
		var publication eventlib.Publication
		if err := json.Unmarshal(doc.Body, &publication); err != nil {
			return err
		}
		publication.Attempts = doc.Attempts
		result = append(result, publication)
		return nil
	})
	return result, err
}

// No goroutines or buffered callbacks: each callback completes before Next.
func (r *reader) scan(ctx context.Context, name string, filter, sort bson.D, limit int64, fn func(document) error) (err error) {
	ctx, done, err := r.bind(ctx)
	if err != nil {
		return err
	}
	defer done()
	cursor, err := r.backend.collection(name).Find(ctx, filter, options.Find().SetSort(sort).SetLimit(limit).SetBatchSize(128))
	if err != nil {
		return storageError(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		err = errors.Join(err, cursor.Close(cleanup))
	}()
	for cursor.Next(ctx) {
		if err = ctx.Err(); err != nil {
			return err
		}
		var doc document
		if err = cursor.Decode(&doc); err != nil {
			return err
		}
		doc.Body, err = r.readBody(ctx, doc)
		if err != nil {
			return err
		}
		if err = fn(doc); err != nil {
			return err
		}
	}
	if err = cursor.Err(); err != nil {
		return storageError(err)
	}
	return ctx.Err()
}
