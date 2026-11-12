package diaruntime

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/runtimeauth"
	"github.com/mejango/blossm-toolbox-go/runtimeenv"
	"github.com/mejango/blossm-toolbox-go/runtimepub"
	"github.com/mejango/blossm-toolbox-go/transport"
	"github.com/mejango/blossm-toolbox-go/wallet"
)

// Run owns the Dia server and its clients until shutdown or failure.
// Runtime configuration comes from the documented environment variables.
func Run(ctx context.Context) (resultErr error) {
	stage := "configuration"
	defer func() {
		if resultErr != nil {
			resultErr = &runtimeFailure{stage: stage, cause: resultErr}
		}
	}()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	network, e := runtimeenv.Required("NETWORK")
	if e != nil {
		return e
	}
	namespace, e := runtimeenv.Required("DATABASE_NAMESPACE")
	if e != nil {
		return e
	}
	local, e := strconv.ParseBool(os.Getenv("LOCAL_MODE"))
	if e != nil {
		return errors.New("LOCAL_MODE must be explicitly true or false")
	}
	stage = "database"
	database, e := runtimeenv.LoadDatabase()
	if e != nil {
		return e
	}
	backend, e := database.Open(ctx, namespace)
	if e != nil {
		return e
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = backend.Close(cleanup)
	}()
	stage = "signing and encryption configuration"
	jwt, closeSigning, e := runtimeauth.Open(ctx, network)
	if e != nil {
		return e
	}
	defer closeSigning()
	hmacKey, e := runtimeenv.Secret32("DIA_HMAC_KEY")
	if e != nil {
		return e
	}
	encryptionKey, e := runtimeenv.Secret32("DIA_ENCRYPTION_KEY")
	if e != nil {
		return e
	}
	reducers := dia.Reducers()
	for k, r := range wallet.Reducers() {
		reducers[k] = r
	}
	store := &eventstore.Store{Backend: backend, Reducers: reducers, Local: local}
	app := &dia.Service{Store: store, Network: network, JWT: jwt, HMACKey: hmacKey, EncryptionKey: encryptionKey}
	stage = "communications configuration"
	if addr := os.Getenv("SMTP_ADDRESS"); addr != "" {
		app.Mail = dia.SMTPMailer{Addr: addr, Username: os.Getenv("SMTP_USERNAME"), Password: os.Getenv("SMTP_PASSWORD")}
	}
	if sid := os.Getenv("TWILIO_ACCOUNT_SID"); sid != "" {
		token, e := runtimeenv.Required("TWILIO_AUTH_TOKEN")
		if e != nil {
			return e
		}
		from, e := runtimeenv.Required("TWILIO_FROM")
		if e != nil {
			return e
		}
		app.SMS = dia.TwilioSMS{AccountSID: sid, AuthToken: token, From: from}
	}
	if endpoint := os.Getenv("UPDATES_PUBLISH_URL"); endpoint != "" {
		token, e := runtimeenv.Required("UPDATES_PUBLISH_TOKEN")
		if e != nil {
			return e
		}
		app.Updates = dia.GRIPPublisher{URL: endpoint, Token: token}
	}
	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()
	var workersDone <-chan struct{}
	stage = "publication runtime"
	if !local {
		publisher, consume, closePublisher, e := runtimepub.Open(ctx, os.Getenv("PUBLISHER"))
		if e != nil {
			return e
		}
		defer func() {
			cancelWorkers()
			// Join cancelled, context-aware workers before closing their SDKs.
			// The HTTP grace period below is not a forced goroutine kill timeout.
			if workersDone != nil {
				<-workersDone
			}
			closePublisher()
		}()
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		runtimepub.Recover(startup, store, publisher, 100, logger)
		cancel()
		done := make(chan struct{})
		workersDone = done
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); runtimepub.Run(workerCtx, store, publisher, 100, time.Second, logger) }()
		go func() { defer wg.Done(); consume(workerCtx, transport.SessionProjection{Store: store}, logger) }()
		go func() { wg.Wait(); close(done) }()
	}
	stage = "HTTP configuration"
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 {
		return errors.New("invalid PORT")
	}
	stage = "wallet runtime"
	walletAPI, closeWallet, e := walletHandler(ctx, app, store)
	if e != nil {
		return e
	}
	defer closeWallet()
	mux := http.NewServeMux()
	mux.Handle("/commands/wallet/", walletAPI)
	mux.Handle("/", transport.API{Dia: app, Store: store}.Handler())
	stage = "HTTP configuration"
	var origins []string
	if raw := os.Getenv("CORS_ORIGINS"); raw != "" {
		if e = json.Unmarshal([]byte(raw), &origins); e != nil {
			return errors.New("invalid CORS_ORIGINS")
		}
	}
	handler, e := transport.CORS(mux, origins)
	if e != nil {
		return e
	}
	stage = "HTTP server"
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Addr: ":" + port, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 35 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	errorsCh := make(chan error, 1)
	go func() { errorsCh <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
	case e = <-errorsCh:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	}
	cancelWorkers()
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	e = server.Shutdown(shutdown)
	if e != nil {
		e = errors.Join(e, server.Close())
	}
	if workersDone != nil {
		select {
		case <-workersDone:
		case <-shutdown.Done():
			e = errors.Join(e, shutdown.Err())
		}
	}
	return e
}
