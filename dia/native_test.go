//go:build integration

package dia

import (
	"blossm.local/backend/eventstore"
	mongoStore "blossm.local/backend/storage/mongo"
	"blossm.local/backend/storage/postgres"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNativeSessionStart(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Fatal("POSTGRES_URL required")
	}
	backend, e := postgres.Open(ctx, url, fmt.Sprintf("dia_%d", time.Now().UnixNano()))
	if e != nil {
		t.Fatal(e)
	}
	defer backend.Close(ctx)
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{Store: &eventstore.Store{Backend: backend, Reducers: Reducers()}, Network: "local", JWT: &JWT{Signer: LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.local", Audience: "local"}}
	secret, e := NewSessionSecret()
	if e != nil {
		t.Fatal(e)
	}
	result, e := s.StartSession(ctx, secret, Device{Type: "desktop", Platform: "browser", Agent: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.AuthenticateAccess(ctx, result.Token); e != nil {
		t.Fatal(e)
	}
}

func TestNativeAccountAndSystemJourney(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			namespace := fmt.Sprintf("dia_identity_%d", time.Now().UnixNano())
			var backend eventstore.Backend
			var e error
			if kind == "postgres" {
				url := os.Getenv("POSTGRES_URL")
				if url == "" {
					t.Fatal("POSTGRES_URL required")
				}
				backend, e = postgres.Open(ctx, url, namespace)
			} else {
				uri := os.Getenv("MONGODB_URI")
				if uri == "" {
					t.Fatal("MONGODB_URI required")
				}
				backend, e = mongoStore.Open(ctx, mongoStore.Config{URI: uri, Database: "blossm_test", Namespace: namespace})
			}
			if e != nil {
				t.Fatal(e)
			}
			defer backend.Close(ctx)
			s, _, mail := coreService(t)
			s.Store = &eventstore.Store{Backend: diagnosticBackend{Backend: backend, t: t}, Reducers: Reducers()}
			start, _ := startCore(t, s)
			challenge, e := s.SaveSession(ctx, start.Token, "save", "person@example.com", "")
			if e != nil {
				t.Fatal(e)
			}
			code := strings.Fields(mail.messages[0].Body)[0]
			type answerResult struct {
				session SessionResult
				err     error
			}
			answers := make(chan answerResult, 2)
			gate := make(chan struct{})
			for i := 0; i < 2; i++ {
				go func() {
					<-gate
					result, err := s.AnswerChallenge(ctx, challenge.Token, "answer", code)
					answers <- answerResult{result, err}
				}()
			}
			close(gate)
			var account SessionResult
			for i := 0; i < 2; i++ {
				result := <-answers
				if result.err != nil {
					t.Fatal("concurrent same-key answer", result.err)
				}
				account = result.session
			}
			if _, e = s.AuthenticateAccess(ctx, start.Token); !errors.Is(e, ErrUnauthorized) {
				t.Fatal("stale anonymous token", e)
			}
			if e = s.SetPassword(ctx, account.Token, "password", "a strong password"); e != nil {
				t.Fatal(e)
			}
			node, e := s.RegisterNode(ctx, account.Token, "node", NodeInput{Network: "app.test"})
			if e != nil {
				t.Fatal(e)
			}
			key, e := s.RegisterKey(ctx, node.Token, "access", "key", KeyInput{Node: node.Node, Name: "integration", Roles: []string{"NodeNetwork"}})
			if e != nil {
				t.Fatal(e)
			}
			connection, e := s.OpenConnection(ctx, ConnectionInput{Key: key.Key, Secret: key.Secret})
			if e != nil {
				t.Fatal(e)
			}
			updates, e := s.IssueUpdatesToken(ctx, connection.Token, "connection")
			if e != nil {
				t.Fatal(e)
			}
			if _, e = s.UpdatesChannel(ctx, updates, "connection-updates", ChannelInput{Name: "sessions"}); e != nil {
				t.Fatal(e)
			}
			if e = s.RevokeKey(ctx, node.Token, "access", "revoke", key.Key); e != nil {
				t.Fatal(e)
			}
			if _, e = s.UpdatesChannel(ctx, updates, "connection-updates", ChannelInput{Name: "sessions"}); !errors.Is(e, ErrUnauthorized) {
				t.Fatal("revoked key accepted", e)
			}
			scene, e := s.RegisterScene(ctx, node.Token, "document-scene", s.root("example", "document", "document"))
			if e != nil {
				t.Fatal(e)
			}
			friend := s.root("core", "principal", "friend")
			group, e := s.AddGroupPrincipals(ctx, node.Token, "group", Ref{}, []GroupPrincipal{{Principal: friend, Roles: []string{"GroupMember"}}})
			if e != nil {
				t.Fatal(e)
			}
			principals, e := s.GroupPrincipals(ctx, node.Token, group)
			if e != nil || len(principals) != 2 {
				t.Fatal(principals, e)
			}
			if e = s.RemovePrincipals(ctx, node.Token, "remove", group, []Ref{friend}); e != nil {
				t.Fatal(e)
			}
			if e = s.CreateRole(ctx, node.Token, "reader", "DocumentReader", []string{"core:scene:read"}); e != nil {
				t.Fatal(e)
			}
			if e = s.ChangeRoles(ctx, node.Token, "grant", account.Context.Principal, []RoleGrant{{ID: "DocumentReader", Subject: scene.Scene}}, false); e != nil {
				t.Fatal(e)
			}
			entered, e := s.ChangeScene(ctx, node.Token, "enter", scene.Scene)
			if e != nil {
				t.Fatal(e)
			}
			stranger, _ := startCore(t, s)
			if e = s.DeleteScene(ctx, stranger.Token, "unauthorized", scene.Scene); !errors.Is(e, ErrForbidden) {
				t.Fatal("unauthorized mutation", e)
			}
			if e = s.DeleteScene(ctx, entered.Token, "delete", scene.Scene); e != nil {
				t.Fatal(e)
			}
			if _, e = s.AuthenticateAccess(ctx, entered.Token); !errors.Is(e, ErrUnauthorized) {
				t.Fatal("deleted scene accepted", e)
			}

		})
	}
}
