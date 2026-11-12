package runtimepub

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"blossm.local/backend/eventstore"
)

type publishFunc func(context.Context, eventstore.Publication) error

func (f publishFunc) Publish(ctx context.Context, p eventstore.Publication) error { return f(ctx, p) }

type Consumer func(context.Context, eventstore.Publisher, *slog.Logger)

var ErrClosed = errors.New("publication runtime closed")

const deliveryLease = 60 * time.Second

// Options bounds each transport/handler phase. Zero values select defaults.
// One delivery is processed at a time within a 60-second provider lease.
// Handlers must honor context cancellation; no goroutine can forcibly stop one.
type Options struct{ PullTimeout, HandleTimeout, AckTimeout, RetryDelay time.Duration }

func (o Options) defaults() (Options, error) {
	if o.PullTimeout == 0 {
		o.PullTimeout = 30 * time.Second
	}
	if o.HandleTimeout == 0 {
		o.HandleTimeout = 45 * time.Second
	}
	if o.AckTimeout == 0 {
		o.AckTimeout = 10 * time.Second
	}
	if o.RetryDelay == 0 {
		o.RetryDelay = time.Second
	}
	if o.PullTimeout < 0 || o.HandleTimeout < 0 || o.AckTimeout < 0 || o.RetryDelay < 0 || o.HandleTimeout >= deliveryLease || o.AckTimeout >= deliveryLease-o.HandleTimeout {
		return o, errors.New("positive phase timeouts required; handler plus acknowledgment must be below 60 seconds")
	}
	return o, nil
}

// Runtime owns the injected provider clients after successful construction.
// Close cancels active operations, joins them, then closes owned clients once.
// Call Close from the owner, never from inside a handler running on this Runtime.
type Runtime struct {
	publish      func(context.Context, []byte, string) error
	consume      Consumer
	closeClients func() error
	ctx          context.Context
	cancel       context.CancelFunc
	mu           sync.Mutex
	closed       bool
	active       sync.WaitGroup
	once         sync.Once
	closeErr     error
}

func newRuntime() *Runtime {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runtime{ctx: ctx, cancel: cancel}
}
func (r *Runtime) begin(ctx context.Context) (context.Context, func(), error) {
	if ctx == nil {
		return nil, nil, errors.New("context required")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, nil, ErrClosed
	}
	r.active.Add(1)
	r.mu.Unlock()
	operation, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(r.ctx, cancel)
	// AfterFunc runs asynchronously; do not start a provider call after closure.
	if r.ctx.Err() != nil {
		cancel()
	}
	return operation, func() { stop(); cancel(); r.active.Done() }, nil
}
func (r *Runtime) Publish(ctx context.Context, notice eventstore.Publication) error {
	operation, done, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	if err := operation.Err(); err != nil {
		return err
	}
	// Encode first, then verify the exact frozen representation being sent.
	data, err := eventstore.Encode(notice)
	if err != nil {
		return err
	}
	frozen, err := decodeNotice(data, "")
	if err != nil {
		return err
	}
	if err := eventstore.ValidatePublicationSize(frozen); err != nil {
		return err
	}
	return r.publish(operation, data, frozen.ID)
}
func (r *Runtime) Consume(ctx context.Context, handler eventstore.Publisher, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	if missing(handler) {
		logger.Error("publication handler required")
		return
	}
	operation, done, err := r.begin(ctx)
	if err != nil {
		return
	}
	defer done()
	r.consume(operation, handler, logger)
}
func (r *Runtime) Close() error {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.cancel()
		r.mu.Unlock()
		r.active.Wait()
		if r.closeClients != nil {
			r.closeErr = r.closeClients()
		}
	})
	return r.closeErr
}
func missing(v any) bool {
	if v == nil {
		return true
	}
	r := reflect.ValueOf(v)
	return (r.Kind() == reflect.Pointer || r.Kind() == reflect.Func || r.Kind() == reflect.Interface) && r.IsNil()
}
func decodeNotice(data []byte, metadataID string) (eventstore.Publication, error) {
	var n eventstore.Publication
	if err := eventstore.Decode(data, &n); err != nil {
		return n, errors.New("invalid event notice encoding")
	}
	if n.ID == "" || n.ID != n.Event.Hash || (metadataID != "" && metadataID != n.ID) || eventstore.Verify(n.Event) != nil {
		return n, errors.New("invalid event notice integrity")
	}
	return n, nil
}
func eventID(value string, present bool) (string, error) {
	if present && value == "" {
		return "", errors.New("empty publication event identity")
	}
	return value, nil
}
func handle(ctx context.Context, timeout time.Duration, handler eventstore.Publisher, body []byte, id string) error {
	notice, err := decodeNotice(body, id)
	if err != nil {
		return err
	}
	work, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := work.Err(); err != nil {
		return err
	}
	if err := handler.Publish(work, notice); err != nil {
		return err
	}
	// A handler returning nil after its deadline has no timely completion proof.
	return work.Err()
}
func pause(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
