// Package mongo implements eventstore persistence on a MongoDB replica set.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
)

// Config has no implicit database or namespace. MaxAttempts includes the first
// attempt; zero selects five. Only Update callbacks may repeat after a known abort.
type Config struct {
	URI         string
	Database    string
	Namespace   string
	MaxAttempts int
}

type Backend struct {
	client   *driver.Client
	db       *driver.Database
	prefix   string
	attempts int
}

var _ eventlib.Backend = (*Backend)(nil)
var validName = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)
var retryTransaction = errors.New("retry aborted transaction")

// Open owns its client, checks replica-set support and awaits every index.
func Open(ctx context.Context, cfg Config) (*Backend, error) {
	if cfg.URI == "" || !validName.MatchString(cfg.Database) || len(cfg.Database) > 63 || !validName.MatchString(cfg.Namespace) || len(cfg.Namespace) > 80 {
		return nil, errors.New("mongo: URI and explicit database/namespace (letters, digits, _ or -) required")
	}
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = 5
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 100 {
		return nil, errors.New("mongo: MaxAttempts must be 1..100")
	}
	client, err := driver.Connect(options.Client().ApplyURI(cfg.URI).SetTimeout(30 * time.Second))
	if err != nil {
		return nil, err
	}
	b := &Backend{client: client, db: client.Database(cfg.Database), prefix: cfg.Namespace + "_", attempts: cfg.MaxAttempts}
	if err = b.initialize(ctx); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return nil, errors.Join(err, client.Disconnect(cleanup))
	}
	return b, nil
}

func (b *Backend) collection(name string) *driver.Collection { return b.db.Collection(b.prefix + name) }
func (b *Backend) Close(ctx context.Context) error           { return b.client.Disconnect(ctx) }

func (b *Backend) initialize(ctx context.Context) error {
	var hello struct {
		SetName  string `bson:"setName"`
		Sessions *int64 `bson:"logicalSessionTimeoutMinutes"`
	}
	if err := b.db.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return err
	}
	if hello.SetName == "" || hello.Sessions == nil {
		return errors.New("mongo: a replica set with sessions is required")
	}
	indexes := map[string][]driver.IndexModel{
		"events":         {unique("hash"), {Keys: bson.D{{Key: "root", Value: 1}, {Key: "number", Value: 1}}, Options: options.Index().SetUnique(true)}},
		"snapshots":      {{Keys: bson.D{{Key: "root", Value: 1}, {Key: "number", Value: -1}}, Options: options.Index().SetUnique(true)}},
		"blocks":         {unique("hash")},
		"publications":   {{Keys: bson.D{{Key: "attempts", Value: 1}, {Key: "_id", Value: 1}}}},
		"counts":         {{Keys: scopeIndex()}},
		"snapshot_heads": {{Keys: append(scopeIndex(), bson.E{Key: "state_terms", Value: 1})}, {Keys: append(scopeIndex(), bson.E{Key: "unindexed", Value: 1})}},
		"payload_chunks": {{Keys: bson.D{{Key: "blob", Value: 1}, {Key: "part", Value: 1}}, Options: options.Index().SetUnique(true)}},
		"projections":    {{Keys: append(scopeIndex(), bson.E{Key: "deleted", Value: 1}, bson.E{Key: "_id", Value: 1})}},
	}
	for name, models := range indexes {
		err := b.db.CreateCollection(ctx, b.prefix+name)
		var command driver.CommandError
		if err != nil && !(errors.As(err, &command) && command.Code == 48) {
			return err
		}
		if len(models) > 0 {
			if _, err = b.collection(name).Indexes().CreateMany(ctx, models); err != nil {
				return err
			}
		}
	}
	return nil
}
func unique(field string) driver.IndexModel {
	return driver.IndexModel{Keys: bson.D{{Key: field, Value: 1}}, Options: options.Index().SetUnique(true)}
}

// View never replays a consumer callback, including already-delivered streams.
func (b *Backend) View(ctx context.Context, fn func(eventlib.Reader) error) error {
	return b.run(ctx, 1, func(t *transaction) error { return fn(&t.reader) })
}
func (b *Backend) Update(ctx context.Context, fn func(eventlib.Tx) error) error {
	return b.run(ctx, b.attempts, func(t *transaction) error { return fn(t) })
}

func (b *Backend) run(ctx context.Context, attempts int, fn func(*transaction) error) error {
	session, err := b.client.StartSession()
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		session.EndSession(c)
	}()
	for attempt := 0; attempt < attempts; attempt++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = session.StartTransaction(options.Transaction().SetReadConcern(readconcern.Snapshot()).SetWriteConcern(writeconcern.Majority()).SetReadPreference(readpref.Primary()))
		if err != nil {
			return err
		}
		err = b.attempt(ctx, session, fn)
		if err == nil || errors.Is(err, eventlib.ErrUncertain) || !retryable(err) {
			return err
		}
		if attempt+1 < attempts {
			timer := time.NewTimer(time.Duration(attempt+1) * 5 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return err
}

func (b *Backend) attempt(ctx context.Context, s *driver.Session, fn func(*transaction) error) error {
	t := &transaction{reader: reader{backend: b, session: s, ctx: ctx, active: true}}
	defer func() { t.active = false }()
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.AbortTransaction(c)
	}()
	if err := fn(t); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := s.CommitTransaction(ctx)
	// Once a commit may have reached the server, never replay the callback.
	if uncertain(err) {
		return errors.Join(eventlib.ErrUncertain, err)
	}
	return err
}

func hasLabel(err error, label string) bool {
	var labeled interface{ HasErrorLabel(string) bool }
	return errors.As(err, &labeled) && labeled.HasErrorLabel(label)
}
func retryable(err error) bool {
	return errors.Is(err, retryTransaction) || hasLabel(err, "TransientTransactionError")
}
func uncertain(err error) bool {
	return err != nil && (hasLabel(err, "UnknownTransactionCommitResult") || driver.IsNetworkError(err) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}
func storageError(err error) error {
	if errors.Is(err, driver.ErrNoDocuments) {
		return eventlib.ErrNotFound
	}
	if driver.IsDuplicateKeyError(err) {
		return errors.Join(eventlib.ErrConflict, err)
	}
	if err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	return nil
}

func scopeIndex() bson.D {
	return bson.D{{Key: "network", Value: 1}, {Key: "service", Value: 1}, {Key: "domain", Value: 1}}
}
