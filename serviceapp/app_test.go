package serviceapp

import (
	"context"
	"testing"

	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/transport"
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
