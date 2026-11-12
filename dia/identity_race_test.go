package dia

import (
	"context"
	"errors"
	"strings"
	"testing"

	"blossm.local/backend/eventstore"
)

// These one-shot gates run a competing operation at an exact read/commit
// boundary. The underlying fake applies real reducers and atomic version checks.
type identityRaceStore struct {
	*systemTestStore
	afterQuery   func()
	beforeAppend func()
}

func (s *identityRaceStore) Query(ctx context.Context, network, service, domain string, predicates []eventstore.Predicate) ([]eventstore.Aggregate, error) {
	rows, err := s.systemTestStore.Query(ctx, network, service, domain, predicates)
	if err == nil && s.afterQuery != nil {
		gate := s.afterQuery
		s.afterQuery = nil
		gate()
	}
	return rows, err
}

func (s *identityRaceStore) Append(ctx context.Context, inputs []eventstore.Input) ([]eventstore.Receipt, error) {
	if s.beforeAppend != nil {
		gate := s.beforeAppend
		s.beforeAppend = nil
		gate()
	}
	return s.systemTestStore.Append(ctx, inputs)
}

func TestDynamicRoleGrantExpansionInterleavings(t *testing.T) {
	for _, grantFirst := range []bool{true, false} {
		name := "expansion_commits_first"
		if grantFirst {
			name = "grant_commits_first"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, store, token := systemTestService(t)
			owner := s.root("core", "principal", "test-principal")
			recipient := s.root("core", "principal", "recipient")
			subject := s.root("core", "scene", "subject")
			otherSubject := s.root("core", "scene", "other-subject")
			role := s.root("core", "role", "DynamicReader")
			store.seed(owner, PrincipalState{Roles: []RoleGrant{{ID: "SceneViewer", Subject: subject}, {ID: "SceneViewer", Subject: otherSubject}}})
			store.seed(role, RoleState{ID: role.ID, Network: s.Network, Permissions: []string{"core:scene:read"}, Principal: owner, Subject: owner})
			original := store.snapshot(role)
			gated := &identityRaceStore{systemTestStore: store}
			s.Store = gated
			grants := []RoleGrant{{ID: role.ID, Subject: subject}, {ID: role.ID, Subject: otherSubject}}
			grant := func() error { return s.ChangeRoles(ctx, token, "grant", recipient, grants, false) }
			expand := func() error {
				return s.ChangeRolePermissions(ctx, token, "expand", role.ID, []string{"core:scene:delete"}, false)
			}
			var winnerErr, loserErr error
			if grantFirst {
				// Expansion reads no grants, but grant commits before expansion appends.
				gated.afterQuery = func() { winnerErr = grant() }
				loserErr = expand()
			} else {
				// Grant authorizes read-only permissions, then expansion wins before commit.
				gated.beforeAppend = func() { winnerErr = expand() }
				loserErr = grant()
			}
			if winnerErr != nil {
				t.Fatalf("winner: %v", winnerErr)
			}
			if !errors.Is(loserErr, eventstore.ErrConflict) {
				t.Fatalf("loser must conflict: %v", loserErr)
			}
			if len(store.batches) != 1 {
				t.Fatalf("partial/extra commits: %d", len(store.batches))
			}
			if store.snapshot(role).Version != original.Version+1 {
				t.Fatal("role must advance exactly once")
			}
			if grantFirst {
				if string(store.snapshot(role).State) != string(original.State) {
					t.Fatal("touch changed permissions")
				}
				var got PrincipalState
				if err := decode(store.snapshot(recipient).State, &got); err != nil {
					t.Fatal(err)
				}
				if len(got.Roles) != 2 {
					t.Fatalf("grants: %+v", got.Roles)
				}
				touches := 0
				for _, in := range store.batches[0] {
					if in.Root == role {
						touches++
						if in.Action != "touch" || string(in.Payload) != "{}" || in.Expected == nil || *in.Expected != original.Version+1 {
							t.Fatalf("touch: %+v", in)
						}
					}
				}
				if touches != 1 {
					t.Fatalf("same role touched %d times", touches)
				}
				if err := grant(); err != nil {
					t.Fatalf("exact retry: %v", err)
				}
				if len(store.batches) != 1 {
					t.Fatal("retry committed another touch")
				}
				if err := expand(); err == nil {
					t.Fatal("expansion retry ignored committed grant")
				}
			} else {
				if store.snapshot(recipient).Version != -1 {
					t.Fatal("conflicting grant partially committed")
				}
				if _, committed, err := s.operation(ctx, recipient, "add-roles", "grant"); err != nil || committed {
					t.Fatalf("conflicting grant receipt: %v %v", committed, err)
				}
				if err := grant(); !errors.Is(err, ErrForbidden) {
					t.Fatalf("retry must authorize expanded permissions: %v", err)
				}
			}
		})
	}
}

func TestBuiltinRoleGrantDoesNotTouchDefinition(t *testing.T) {
	ctx := context.Background()
	s, store, token := systemTestService(t)
	owner := s.root("core", "principal", "test-principal")
	recipient := s.root("core", "principal", "recipient")
	subject := s.root("core", "scene", "subject")
	store.seed(owner, PrincipalState{Roles: []RoleGrant{{ID: "SceneViewer", Subject: subject}}})
	if err := s.ChangeRoles(ctx, token, "grant", recipient, []RoleGrant{{ID: "SceneViewer", Subject: subject}}, false); err != nil {
		t.Fatal(err)
	}
	if len(store.batches) != 1 || len(store.batches[0]) != 2 {
		t.Fatal("builtin grant should contain principal and receipt only")
	}
	for _, in := range store.batches[0] {
		if in.Root.Domain == "role" {
			t.Fatal("builtin definition touched")
		}
	}
}

func TestIdentityMutationsRejectInvalidKeysBeforeAuthentication(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	for name, call := range map[string]func(string) error{
		"DeleteScene":           func(key string) error { return s.DeleteScene(ctx, "", key, Ref{}) },
		"RemovePrincipals":      func(key string) error { return s.RemovePrincipals(ctx, "", key, Ref{}, nil) },
		"ChangeRoles":           func(key string) error { return s.ChangeRoles(ctx, "", key, Ref{}, nil, false) },
		"CreateRole":            func(key string) error { return s.CreateRole(ctx, "", key, "role", nil) },
		"ChangeRolePermissions": func(key string) error { return s.ChangeRolePermissions(ctx, "", key, "role", nil, false) },
	} {
		t.Run(name, func(t *testing.T) {
			for _, key := range []string{"", " \t\n", strings.Repeat("k", 257)} {
				if err := call(key); !errors.Is(err, ErrInvalid) {
					t.Fatalf("invalid key accepted: %v", err)
				}
			}
		})
	}
}
