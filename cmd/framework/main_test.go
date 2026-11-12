package main

import (
	"context"
	"errors"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type countingPublisher struct{ calls atomic.Int32 }

func (p *countingPublisher) Publish(context.Context, eventstore.Publication) error {
	p.calls.Add(1)
	return nil
}
func TestPublicationRouting(t *testing.T) {
	documents, identity := &countingPublisher{}, &countingPublisher{}
	router := publicationRouter{documents: documents, identity: identity, network: "network", service: "notes"}
	for _, tc := range []struct {
		network, service, domain string
		want                     error
	}{{"network", "notes", "document", nil}, {"network", "core", "session", nil}, {"other", "notes", "document", dia.ErrForbidden}, {"network", "foreign", "document", dia.ErrForbidden}} {
		err := router.Publish(context.Background(), eventstore.Publication{Event: eventstore.Event{Input: eventstore.Input{Root: eventstore.Root{Network: tc.network, Service: tc.service, Domain: tc.domain}}}})
		if !errors.Is(err, tc.want) {
			t.Fatal(err)
		}
	}
	if documents.calls.Load() != 1 || identity.calls.Load() != 1 {
		t.Fatal("wrong publication destination")
	}
}
func TestHTTPServerCancellationJoins(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	exited := make(chan struct{})
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-r.Context().Done(); close(exited) })}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, cancel, server, listener, make(chan error)) }()
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		response, e := http.Get("http://" + listener.Addr().String())
		if e == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
	select {
	case <-exited:
	default:
		t.Fatal("handler not joined")
	}
	<-clientDone
}
