//go:build integration

package dia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/mejango/blossm-toolbox-go/eventstore"
	mongoStore "github.com/mejango/blossm-toolbox-go/storage/mongo"
	"github.com/mejango/blossm-toolbox-go/storage/postgres"
)

func TestNativeRemoveRolesRequestRecovery(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			namespace := fmt.Sprintf("remove_roles_%d", time.Now().UnixNano())
			var backend eventstore.Backend
			var err error
			if kind == "postgres" {
				if os.Getenv("POSTGRES_URL") == "" {
					t.Fatal("POSTGRES_URL required")
				}
				backend, err = postgres.Open(ctx, os.Getenv("POSTGRES_URL"), namespace)
			} else {
				if os.Getenv("MONGODB_URI") == "" {
					t.Fatal("MONGODB_URI required")
				}
				backend, err = mongoStore.Open(ctx, mongoStore.Config{URI: os.Getenv("MONGODB_URI"), Database: "blossm_test", Namespace: namespace})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close(ctx)
			s, _, _ := coreService(t)
			store := &eventstore.Store{Backend: backend, Reducers: Reducers()}
			s.Store = store
			makeNode := func(id string) NodeResult {
				t.Helper()
				start, _ := startCore(t, s)
				node, e := s.RegisterNode(ctx, start.Token, id, NodeInput{Network: id + ".test"})
				if e != nil {
					t.Fatal(e)
				}
				return node
			}
			node := makeNode("owner")
			delegate := makeNode("delegate")
			stranger := makeNode("stranger")
			grants := []RoleGrant{{ID: "GroupAdmin", Subject: node.Group}}
			if err = s.ChangeRoles(ctx, node.Token, "delegate", delegate.Principal, grants, false); err != nil {
				t.Fatal(err)
			}
			subjects := []Ref{node.Group}
			explicit := []RoleGrant{{ID: "GroupAdmin", Subject: node.Group}}
			// Both callers may prepare against the old state; only one removal commits.
			gate := make(chan struct{})
			results := make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() {
					<-gate
					results <- s.RemoveRoles(ctx, node.Token, "remove", node.Principal, explicit, subjects)
				}()
			}
			close(gate)
			for i := 0; i < 2; i++ {
				if e := <-results; e != nil {
					t.Fatal("identical concurrent retry", e)
				}
			}
			before, err := store.Aggregate(ctx, node.Principal)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", node.Principal, explicit, subjects); err != nil {
				t.Fatal("self-role receipt recovery", err)
			}
			after, _ := store.Aggregate(ctx, node.Principal)
			if after.Version != before.Version {
				t.Fatal("receipt replayed mutation")
			}
			if err = s.RemoveRoles(ctx, node.Token, "new-action", node.Principal, explicit, subjects); !errors.Is(err, ErrForbidden) {
				t.Fatal("receipt authority used for new action", err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "empty-new-action", node.Principal, nil, subjects); !errors.Is(err, ErrForbidden) {
				t.Fatal("empty expansion created unauthorized no-op", err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", node.Principal, nil, subjects); !errors.Is(err, eventstore.ErrIdempotency) {
				t.Fatal("changed explicit request accepted", err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", node.Principal, explicit, []Ref{node.Node}); !errors.Is(err, eventstore.ErrIdempotency) {
				t.Fatal("changed subject request accepted", err)
			}
			if err = s.RemoveRoles(ctx, stranger.Token, "remove", node.Principal, explicit, subjects); !errors.Is(err, ErrForbidden) {
				t.Fatal("other caller recovered receipt", err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", stranger.Principal, []RoleGrant{{ID: "GroupAdmin", Subject: stranger.Group}}, []Ref{stranger.Group}); !errors.Is(err, eventstore.ErrIdempotency) {
				t.Fatal("changed target used recovery authority", err)
			}
			// A later authorized grant must survive an old removal retry.
			if err = s.ChangeRoles(ctx, delegate.Token, "regrant", node.Principal, grants, false); err != nil {
				t.Fatal(err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", delegate.Principal, explicit, subjects); !errors.Is(err, eventstore.ErrIdempotency) {
				t.Fatal("same key authorized target change accepted", err)
			}
			before, err = store.Aggregate(ctx, node.Principal)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", node.Principal, explicit, subjects); err != nil {
				t.Fatal(err)
			}
			after, _ = store.Aggregate(ctx, node.Principal)
			if after.Version != before.Version || string(after.State) != string(before.State) {
				t.Fatal("old removal consumed regrant")
			}
			// The receipt keeps exact original targets and requested scope, not current roles.
			scopedKey := stableID(nodePrincipalSession(t, s, node.Token).Key(), "remove-roles-request/remove")
			var op operationState
			receipt, err := s.load(ctx, s.root("core", "operation", stableID(scopedKey, "remove-roles-receipt")), &op)
			if err != nil || receipt.Version == 0 || len(op.Request) == 0 || len(op.Inputs) == 0 {
				t.Fatal("missing semantic receipt", err)
			}
			if err = s.TerminateSession(ctx, node.Token, "terminate"); err != nil {
				t.Fatal(err)
			}
			if err = s.RemoveRoles(ctx, node.Token, "remove", node.Principal, explicit, subjects); !errors.Is(err, ErrUnauthorized) {
				t.Fatal("revoked session recovered receipt", err)
			}
		})
	}
}

func nodePrincipalSession(t *testing.T, s *Service, token string) Ref {
	t.Helper()
	claims, err := s.AuthenticateAccess(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return claims.Context.Session
}
