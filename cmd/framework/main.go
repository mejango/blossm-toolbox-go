// Command framework serves the composed generic application over ordinary HTTP.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/runtimeauth"
	"github.com/mejango/blossm-toolbox-go/runtimepub"
	"github.com/mejango/blossm-toolbox-go/runtimequeue"
	"github.com/mejango/blossm-toolbox-go/serviceapp"
	"github.com/mejango/blossm-toolbox-go/transport"
)

func run(ctx context.Context) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	jwt, closeSigning, err := runtimeauth.Open(ctx, cfg.Network)
	if err != nil {
		return err
	}
	defer closeSigning()
	internal, err := transport.ServiceJWTVerifier(*jwt, cfg.InternalSubject)
	if err != nil {
		return err
	}
	identityBackend, err := cfg.Database.Open(ctx, cfg.IdentityNamespace)
	if err != nil {
		return err
	}
	defer closeBackend(identityBackend)
	documentBackend, err := cfg.Database.Open(ctx, cfg.DocumentNamespace)
	if err != nil {
		return err
	}
	defer closeBackend(documentBackend)
	local := cfg.Mode == "local"
	identityStore := &eventstore.Store{Backend: identityBackend, Reducers: dia.Reducers(), Local: local}
	identity := &dia.Service{Store: identityStore, Network: cfg.Network, JWT: jwt, HMACKey: cfg.HMACKey, EncryptionKey: cfg.EncryptionKey}
	logger := runtimeLogger(os.Stdout)
	appConfig := serviceapp.Config{Store: &eventstore.Store{Backend: documentBackend, Local: local}, Identity: identity, Service: cfg.Service, Internal: internal, InlinePublication: cfg.Mode == "inline", OnRejected: rejectionLogger(logger)}
	peers, err := configurePeers(cfg, *jwt, identity)
	if err != nil {
		return err
	}
	defer peers.close()
	appConfig.Outbound, appConfig.Delegated = peers.outbound, peers.delegated
	var consume runtimepub.Consumer
	var queue *runtimequeue.Runtime
	if cfg.Mode == "gcp" || cfg.Mode == "aws" {
		publisher, consumer, closePublisher, e := runtimepub.Open(ctx, cfg.Mode)
		if e != nil {
			return e
		}
		defer closePublisher()
		appConfig.Publisher = publisher
		consume = consumer
		queue, e = runtimequeue.Open(ctx, cfg.Mode)
		if e != nil {
			return e
		}
		defer queue.Close()
		appConfig.Queue = queue.Commands
		appConfig.Replay = queue.Replay
		appConfig.DeliveryVerifier = queue.DeliveryVerifier
	}
	app, err := serviceapp.New(ctx, appConfig)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("/", app.Handler)
	handler, err := transport.CORS(mux, cfg.Origins)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		return err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	failures := make(chan error, 1)
	start := func(fn func() error) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if e := fn(); e != nil && workerCtx.Err() == nil {
				select {
				case failures <- e:
				default:
				}
				cancel()
			}
		}()
	}
	// Cleanup joins every owned worker before closing any borrowed SDK/backend.
	defer func() { cancel(); workers.Wait() }()
	if !local {
		identityProjection := transport.SessionProjection{Store: identityStore}
		identityPublisher := eventstore.Publisher(identityProjection)
		if consume != nil {
			identityPublisher = app.Publisher
		}
		start(func() error {
			runtimepub.Recover(workerCtx, app.Store, app.Publisher, 100, logger)
			runtimepub.Run(workerCtx, app.Store, app.Publisher, 100, time.Second, logger)
			return nil
		})
		start(func() error {
			runtimepub.Recover(workerCtx, identityStore, identityPublisher, 100, logger)
			runtimepub.Run(workerCtx, identityStore, identityPublisher, 100, time.Second, logger)
			return nil
		})
		if consume != nil {
			start(func() error {
				consume(workerCtx, publicationRouter{documents: app.Publication, identity: identityProjection, network: cfg.Network, service: cfg.Service}, logger)
				return nil
			})
		}
	}
	if queue != nil {
		start(func() error { return queue.Run(workerCtx, app.Delivery, logger) })
	}
	server := &http.Server{Handler: handler, BaseContext: func(net.Listener) context.Context { return workerCtx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	return serve(workerCtx, cancel, server, listener, failures)
}

func closeBackend(backend eventstore.Backend) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = backend.Close(ctx)
}

type publicationRouter struct {
	documents        eventstore.Publisher
	identity         eventstore.Publisher
	network, service string
}

func (r publicationRouter) Publish(ctx context.Context, p eventstore.Publication) error {
	root := p.Event.Input.Root
	if root.Network != r.network {
		return dia.ErrForbidden
	}
	if root.Service == r.service && root.Domain == serviceapp.Domain {
		return r.documents.Publish(ctx, p)
	}
	if root.Service == "core" || root.Service == "system" || root.Service == "comms" {
		return r.identity.Publish(ctx, p)
	}
	return dia.ErrForbidden
}

func serve(ctx context.Context, cancel context.CancelFunc, server *http.Server, listener net.Listener, failures <-chan error) error {
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	var result error
	served := false
	select {
	case <-ctx.Done():
		select {
		case result = <-failures:
		default:
		}
	case result = <-failures:
	case result = <-done:
		served = true
	}
	if errors.Is(result, http.ErrServerClosed) {
		result = nil
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	if err := server.Shutdown(shutdown); err != nil {
		result = errors.Join(result, err, server.Close())
	}
	if !served {
		if err := <-done; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = errors.Join(result, err)
		}
	}
	return result
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx); err != nil {
		slog.Error("framework stopped", "deadline", errors.Is(err, context.DeadlineExceeded), "canceled", errors.Is(err, context.Canceled))
		os.Exit(1)
	}
}
