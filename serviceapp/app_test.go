package serviceapp

import (
	"context"
	"testing"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/transport"
)

type unusedBackend struct{ eventstore.Backend }

func TestRejectSharedOutboxBackend(t *testing.T) {
	backend := &unusedBackend{}
	identity := &dia.Service{Network: "n", Store: &eventstore.Store{Backend: backend}}
	_, err := New(context.Background(), Config{Store: &eventstore.Store{Backend: backend}, Identity: identity, Service: "example", InlinePublication: true, Internal: func(context.Context, string) (transport.Identity, error) {
		t.Fatal("configuration reached authentication")
		return transport.Identity{}, nil
	}})
	if err == nil {
		t.Fatal("two consumers accepted shared outbox backend")
	}
}
