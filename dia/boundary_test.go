package dia

import (
	"context"
	"errors"
	"testing"
)

func TestDelegatedContextUsesCurrentSessionRules(t *testing.T) {
	for _, name := range []string{"valid", "network", "session-network", "session-domain", "principal", "account", "scene", "expired-token", "future-token", "terminated", "expired-session", "subject-without-scene"} {
		t.Run(name, func(t *testing.T) {
			s, store, token := systemTestService(t)
			claims, err := s.JWT.Verify(token, "access")
			if err != nil {
				t.Fatal(err)
			}
			var state SessionState
			if err = decode(store.snapshot(claims.Context.Session).State, &state); err != nil {
				t.Fatal(err)
			}
			root := claims.Context.Session
			switch name {
			case "network":
				claims.Context.Network = "foreign"
			case "session-network":
				claims.Context.Session.Network = "foreign"
			case "session-domain":
				claims.Context.Session.Domain = "principal"
			case "principal":
				claims.Context.Principal.ID = "other"
			case "account":
				claims.Context.Account = s.root("core", "account", "other")
			case "scene":
				claims.Context.Scene = s.root("core", "scene", "other")
			case "expired-token":
				claims.Expires = s.now().Unix()
			case "future-token":
				claims.NotBefore = s.now().Unix() + 1
			case "terminated":
				state.Terminated = "terminated"
			case "expired-session":
				state.Expires = s.now().Unix()
			case "subject-without-scene":
				claims.Context.Subject = s.root("core", "account", "invented")
			}
			store.seed(root, state)
			accessErr := s.authorizeSessionContext(context.Background(), claims, false)
			firstHopErr := s.AuthorizeDelegatedContext(context.Background(), claims)
			claims.Kind = "service"
			claims.Subject = "verified-peer"
			delegatedErr := s.AuthorizeDelegatedContext(context.Background(), claims)
			if name == "valid" {
				if accessErr != nil || firstHopErr != nil || delegatedErr != nil {
					t.Fatal(accessErr, firstHopErr, delegatedErr)
				}
			} else if !errors.Is(accessErr, ErrUnauthorized) || !errors.Is(firstHopErr, ErrUnauthorized) || !errors.Is(delegatedErr, ErrUnauthorized) {
				t.Fatal("state rules diverged", accessErr, firstHopErr, delegatedErr)
			}
			if claims.Subject != "verified-peer" {
				t.Fatal("peer identity mutated")
			}
		})
	}
}

func TestDelegatedSceneSubjectAndRevocation(t *testing.T) {
	s, store, token := systemTestService(t)
	claims, err := s.JWT.Verify(token, "access")
	if err != nil {
		t.Fatal(err)
	}
	var session SessionState
	if err = decode(store.snapshot(claims.Context.Session).State, &session); err != nil {
		t.Fatal(err)
	}
	claims.Context.Scene = s.root("core", "scene", "scene")
	claims.Context.Subject = s.root("example", "document", "document")
	session.Scene = claims.Context.Scene
	store.seed(claims.Context.Session, session)
	scene := SceneState{Subject: claims.Context.Subject, Group: s.root("core", "group", "group")}
	store.seed(claims.Context.Scene, scene)
	claims.Kind = "service"
	claims.Subject = "verified-peer"
	if err = s.AuthorizeDelegatedContext(context.Background(), claims); err != nil {
		t.Fatal(err)
	}
	changed := claims
	changed.Context.Subject.ID = "other"
	if err = s.AuthorizeDelegatedContext(context.Background(), changed); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("foreign subject accepted", err)
	}
	scene.Deleted = "deleted"
	store.seed(claims.Context.Scene, scene)
	if err = s.AuthorizeDelegatedContext(context.Background(), claims); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("deleted scene accepted", err)
	}
	claims.Kind = "challenge"
	if err = s.AuthorizeDelegatedContext(context.Background(), claims); !errors.Is(err, ErrUnauthorized) {
		t.Fatal("unverified token kind accepted", err)
	}
	claims.Kind = "service"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.AuthorizeDelegatedContext(ctx, claims); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
}
