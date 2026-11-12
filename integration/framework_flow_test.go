//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tasks "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/googleapis/gax-go/v2"
	"github.com/mejango/blossm-toolbox-go/cloud/gcp"
	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/delivery"
	"github.com/mejango/blossm-toolbox-go/dia"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/fact"
	"github.com/mejango/blossm-toolbox-go/projection"
	"github.com/mejango/blossm-toolbox-go/rpc"
	"github.com/mejango/blossm-toolbox-go/serviceapp"
	"github.com/mejango/blossm-toolbox-go/transport"
)

type taskMailbox struct {
	gcp.TasksAPI
	mu     sync.Mutex
	bodies [][]byte
	names  map[string]bool
}

func (q *taskMailbox) CreateTask(ctx context.Context, in *tasks.CreateTaskRequest, _ ...gax.CallOption) (*tasks.Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.names == nil {
		q.names = map[string]bool{}
	}
	if !q.names[in.Task.Name] {
		q.bodies = append(q.bodies, bytes.Clone(in.Task.GetHttpRequest().Body))
		q.names[in.Task.Name] = true
	}
	return in.Task, nil
}
func (q *taskMailbox) pop() []byte {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.bodies) == 0 {
		return nil
	}
	b := q.bodies[0]
	q.bodies = q.bodies[1:]
	return b
}
func frameworkIdentity(t *testing.T, kind string) (*dia.Service, dia.SessionResult, string, transport.TokenVerifier) {
	t.Helper()
	ctx := context.Background()
	s := newStore(t, kind)
	s.Reducers = dia.Reducers()
	s.Local = true
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	jwt := &dia.JWT{Signer: dia.LocalSigner{Key: key}, VerifyKey: &key.PublicKey, Issuer: "dia.local", Audience: "local"}
	identity := &dia.Service{Store: s, Network: "local", JWT: jwt}
	secret, err := dia.NewSessionSecret()
	if err != nil {
		t.Fatal(err)
	}
	session, err := identity.StartSession(ctx, secret, dia.Device{Type: "desktop", Platform: "browser", Agent: "native-framework"})
	if err != nil {
		t.Fatal(err)
	}
	session = documentPrincipal(t, identity, session, "initial")
	peer, err := jwt.Sign(ctx, dia.Claims{Issuer: "dia.local", Audience: []string{"local"}, Subject: "native-worker", ID: "peer", Kind: "service", NotBefore: time.Now().Unix() - 1, Expires: time.Now().Unix() + 3600})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := transport.ServiceJWTVerifier(*jwt, "native-worker")
	if err != nil {
		t.Fatal(err)
	}
	return identity, session, peer, verifier
}
func documentPrincipal(t *testing.T, identity *dia.Service, session dia.SessionResult, key string) dia.SessionResult {
	t.Helper()
	scene, err := identity.RegisterScene(context.Background(), session.Token, key, eventstore.Root{Network: "local", Service: "system", Domain: "node", ID: "documents"})
	if err != nil {
		t.Fatal(err)
	}
	claims, err := identity.JWT.Verify(scene.Token, "access")
	if err != nil {
		t.Fatal(err)
	}
	session.Token = scene.Token
	session.Context = claims.Context
	return session
}
func frameworkRPC(t *testing.T, h http.Handler) (*rpc.Client, *httptest.Server) {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	c, err := rpc.New(s.URL, rpc.Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c, s
}
func bearer(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}
func decodeCommand(t *testing.T, r rpc.Response) command.Response {
	t.Helper()
	var out command.Response
	if err := eventstore.Decode(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGenericFrameworkNativeFlow(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			identity, session, peer, internal := frameworkIdentity(t, kind)
			pushes := 0
			app, err := serviceapp.New(ctx, serviceapp.Config{Store: newStore(t, kind), Identity: identity, Service: "example", Internal: internal, InlinePublication: true, Push: func(context.Context, projection.Notification) error { pushes++; return nil }})
			if err != nil {
				t.Fatal(err)
			}
			client, server := frameworkRPC(t, app.Handler)
			root := eventstore.Root{Network: "local", Service: "example", Domain: serviceapp.Domain, ID: "one"}
			in := command.Request{ID: "set-one", Issued: "2026-11-12T00:00:00Z", Root: &root, Payload: json.RawMessage(`{"text":"hello"}`), Context: command.Context{"principal": json.RawMessage(`{"id":"forged"}`)}}
			first, err := client.Do(ctx, "POST", "/commands/set", nil, in, bearer(session.Token))
			if err != nil {
				t.Fatal(err)
			}
			out := decodeCommand(t, first)
			if len(out.Receipts) != 1 || out.Receipts[0].Outcome != eventstore.Committed || pushes != 1 {
				t.Fatal("missing full command->projection flow", out, pushes)
			}
			original := out.Receipts[0].Event
			claims, err := identity.JWT.Verify(session.Token, "access")
			if err != nil {
				t.Fatal(err)
			}
			claims.ID = "refreshed"
			claims.Expires--
			refreshed, err := identity.JWT.Sign(ctx, claims)
			if err != nil {
				t.Fatal(err)
			}
			second, err := client.Do(ctx, "POST", "/commands/set", nil, in, bearer(refreshed))
			if err != nil {
				t.Fatal("refresh retry", err)
			}
			retry := decodeCommand(t, second)
			if retry.Receipts[0].Outcome != eventstore.Duplicate || !bytes.Equal(retry.Body["id"], out.Body["id"]) || retry.Receipts[0].Event.Hash != original.Hash || pushes != 1 {
				t.Fatal("retry changed commitment/response/push")
			}
			if err = eventstore.Verify(*retry.Receipts[0].Event); err != nil {
				t.Fatal(err)
			}
			v, err := client.Do(ctx, "GET", "/view-stores/documents/one", nil, nil, bearer(session.Token))
			if err != nil {
				t.Fatal(err)
			}
			var row eventstore.Projection
			if err = eventstore.Decode(v.Body, &row); err != nil {
				t.Fatal(err)
			}
			if row.ID != "one" || row.Version != 0 {
				t.Fatal(row)
			}
			aggregate, err := (transport.StoreClient{RPC: client}).Aggregate(ctx, root, bearer(peer))
			if err != nil || aggregate.Version != 0 {
				t.Fatal("remote replay", aggregate, err)
			}
			if _, err = (transport.StoreClient{RPC: client}).Aggregate(ctx, root, bearer(session.Token)); err == nil {
				t.Fatal("user token accepted as internal credential")
			}
			var count int
			err = (fact.Client{RPC: client}).Stream(ctx, "/facts/document", url.Values{"stream": {"true"}}, bearer(session.Token), func(context.Context, json.RawMessage) error { count++; return nil })
			if err != nil || count != 1 {
				t.Fatal("fact stream", count, err)
			}
			_, err = client.Do(ctx, "POST", "/jobs/verify", nil, map[string]any{"payload": map[string]string{"id": "one"}, "context": map[string]string{"principal": "forged"}, "claims": map[string]string{"sub": "forged"}}, bearer(session.Token))
			if err != nil {
				t.Fatal("authenticated job", err)
			}
			// A second authenticated principal cannot read or replace the first owner's row.
			secret, _ := dia.NewSessionSecret()
			other, err := identity.StartSession(ctx, secret, dia.Device{Type: "desktop", Platform: "browser", Agent: "other"})
			if err != nil {
				t.Fatal(err)
			}
			other = documentPrincipal(t, identity, other, "other")
			if _, err = client.Do(ctx, "GET", "/view-stores/documents/one", nil, nil, bearer(other.Token)); err == nil {
				t.Fatal("foreign view visible")
			}
			stolen := in
			stolen.ID = "steal"
			if _, err = client.Do(ctx, "POST", "/commands/set", nil, stolen, bearer(other.Token)); err == nil {
				t.Fatal("foreign owner replaced")
			}
			// A real parent Main invokes the configured second service using
			// a scoped peer signature carrying the authenticated user context.
			delegatedJWT := *identity.JWT
			delegatedJWT.Audience = "local/second"
			delegated, err := transport.DelegatedCommandVerifier(delegatedJWT, identity, "local/example")
			if err != nil {
				t.Fatal(err)
			}
			secondApp, err := serviceapp.New(ctx, serviceapp.Config{Store: newStore(t, kind), Identity: identity, Service: "second", Internal: internal, Delegated: delegated, InlinePublication: true})
			if err != nil {
				t.Fatal(err)
			}
			remote, _ := frameworkRPC(t, secondApp.Handler)
			mint, err := transport.DelegatedCommandAuthorizer(delegatedJWT, identity, "local/example")
			if err != nil {
				t.Fatal(err)
			}
			parentApp, err := serviceapp.New(ctx, serviceapp.Config{Store: &eventstore.Store{Backend: app.Store.Backend}, Identity: identity, Service: "example", Internal: internal, InlinePublication: true,
				Outbound: &transport.CommandClient{RPC: remote, Target: "local/second", Authorize: mint}})
			if err != nil {
				t.Fatal(err)
			}
			parentRPC, _ := frameworkRPC(t, parentApp.Handler)
			parent := command.Request{ID: "copy-one", Issued: in.Issued, Payload: json.RawMessage(`{"source":"one","sourceVersion":0,"destination":"copied"}`)}
			var childID string
			for i, outcome := range []eventstore.Outcome{eventstore.Committed, eventstore.Duplicate} {
				if i == 1 {
					changed := in
					changed.ID = "update-source"
					changed.Payload = json.RawMessage(`{"text":"changed after copy"}`)
					if _, err := client.Do(ctx, "POST", "/commands/set", nil, changed, bearer(session.Token)); err != nil {
						t.Fatal(err)
					}
				}
				response, err := parentRPC.Do(ctx, "POST", "/commands/copy", nil, parent, bearer(session.Token))
				if err != nil {
					t.Fatal("nested Main", err)
				}
				result := decodeCommand(t, response)
				var child command.Response
				if err := eventstore.Decode(result.Body["child"], &child); err != nil {
					t.Fatal(err)
				}
				if len(child.Receipts) != 1 || child.Receipts[0].Outcome != outcome {
					t.Fatal("child outcome", child)
				}
				if i == 0 {
					childID = child.ID
				} else if child.ID != childID {
					t.Fatal("child identity changed")
				}
				event := child.Receipts[0].Event
				var copied serviceapp.Document
				if eventstore.Decode(event.Input.Payload, &copied) != nil || copied.Text != "hello" {
					t.Fatal("copy retry reread mutable source", copied)
				}
				if event.Input.Transaction.ID != parent.ID || event.Input.Transaction.Issued != in.Issued || len(event.Input.Transaction.Path) != 2 || event.Input.Transaction.Path[0].Procedure != "copy" || event.Input.Transaction.Path[1].Procedure != "set" {
					t.Fatal("child lineage", event.Input.Transaction)
				}
				var childClaims dia.Claims
				if eventstore.Decode(event.Input.Transaction.Claims, &childClaims) != nil || childClaims.Kind != "service" || childClaims.Subject != "local/example" || childClaims.Context.Principal != session.Context.Principal {
					t.Fatal("child authority", childClaims)
				}
			}
			authenticatedParent := parent
			authenticatedParent.Claims, _ = eventstore.Encode(claims)
			contextBytes, _ := eventstore.Encode(claims.Context)
			if err := eventstore.Decode(contextBytes, &authenticatedParent.Context); err != nil {
				t.Fatal(err)
			}
			delegatedHeaders, err := mint(ctx, authenticatedParent)
			if err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []struct {
				method, path string
				body         any
			}{
				{"GET", "/facts/document", nil},
				{"GET", "/view-stores/documents", nil},
				{"GET", "/view-stores/documents/copied", nil},
				{"POST", "/jobs/verify", map[string]any{"payload": map[string]string{"id": "copied"}}},
			} {
				_, err := remote.Do(ctx, endpoint.method, endpoint.path, nil, endpoint.body, delegatedHeaders)
				var rpcErr *rpc.HTTPError
				if !errors.As(err, &rpcErr) || rpcErr.StatusCode != http.StatusUnauthorized {
					t.Fatalf("delegation escaped command scope %s: %v", endpoint.path, err)
				}
			}
			// Restart composition over the same database; original event bytes still replay.
			restarted, err := serviceapp.New(ctx, serviceapp.Config{Store: &eventstore.Store{Backend: app.Store.Backend, Reducers: map[string]eventstore.Reducer{}}, Identity: identity, Service: "example", Internal: internal, InlinePublication: true})
			if err != nil {
				t.Fatal(err)
			}
			aggregate, err = app.Store.Aggregate(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			state, err := restarted.Store.Aggregate(ctx, root)
			if err != nil || !bytes.Equal(state.State, aggregate.State) {
				t.Fatal("restart replay", err)
			}
			req, _ := http.NewRequest("POST", server.URL+"/identity/commands/session/terminate", nil)
			req.Header = bearer(session.Token)
			req.Header.Set("Idempotency-Key", "terminate")
			r, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
			if r.StatusCode != 200 {
				t.Fatal(r.Status)
			}
			if _, err = client.Do(ctx, "GET", "/view-stores/documents/one", nil, nil, bearer(session.Token)); err == nil {
				t.Fatal("revoked session can read view")
			}
		})
	}
}

func TestGenericAsyncDeliveryAndReplayNative(t *testing.T) {
	for _, kind := range []string{"postgres", "mongo"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			identity, session, peer, internal := frameworkIdentity(t, kind)
			mailbox := &taskMailbox{}
			target := delivery.TasksTarget{Tasks: gcp.Tasks{Client: mailbox}, Queue: "projects/p/locations/l/queues/q", Dispatch: gcp.HTTPDispatch{URL: "https://worker.invalid/deliveries", ServiceAccount: "worker@example.test"}}
			publisher := delivery.TasksPublications{Target: target}
			queue := delivery.TasksCommands{Target: target}
			replay := delivery.TasksReplay{Target: target}
			deliveryToken, err := identity.JWT.Sign(ctx, dia.Claims{Issuer: "dia.local", Audience: []string{"local"}, Subject: "native-delivery", ID: "delivery", Kind: "service", NotBefore: time.Now().Unix() - 1, Expires: time.Now().Unix() + 3600})
			if err != nil {
				t.Fatal(err)
			}
			deliveryVerifier, err := transport.ServiceJWTVerifier(*identity.JWT, "native-delivery")
			if err != nil {
				t.Fatal(err)
			}
			var observed atomic.Int64
			app, err := serviceapp.New(ctx, serviceapp.Config{OnRejected: func(context.Context, delivery.Rejection) error {
				observed.Add(1)
				return errors.New("test observer unavailable")
			}, Store: newStore(t, kind), Identity: identity, Service: "async", Internal: internal, DeliveryVerifier: deliveryVerifier, Publisher: publisher, Queue: queue, Replay: replay})
			if err != nil {
				t.Fatal(err)
			}
			client, server := frameworkRPC(t, app.Handler)
			root := eventstore.Root{Network: "local", Service: "async", Domain: serviceapp.Domain, ID: "queued"}
			input := command.Request{ID: "async-1", Issued: "2026-11-12T00:00:00Z", Root: &root, Payload: json.RawMessage(`{"text":"queued"}`), Async: true}
			response, err := client.Do(ctx, "POST", "/commands/set", nil, input, bearer(session.Token))
			if err != nil {
				t.Fatal(err)
			}
			out := decodeCommand(t, response)
			if out.Receipts[0].Outcome != eventstore.Queued {
				t.Fatal("not queued")
			}
			count, err := app.Store.Count(ctx, root)
			if err != nil || count != 0 {
				t.Fatal("queue falsely claimed commit")
			}
			consume := func(body []byte) {
				t.Helper()
				req, _ := http.NewRequest("POST", server.URL+"/deliveries", bytes.NewReader(body))
				req.Header = bearer(deliveryToken)
				r, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				r.Body.Close()
				if r.StatusCode != 204 {
					t.Fatal("worker failed", r.Status)
				}
			}
			commandBody := mailbox.pop()
			if commandBody == nil {
				t.Fatal("missing provider dispatch")
			}
			submittedEnvelope, err := delivery.Decode(commandBody)
			if err != nil || out.Acceptance == nil || out.Acceptance.EnvelopeID != submittedEnvelope.ID {
				t.Fatal("queued receipt missing stable envelope identity", err)
			}
			denied, _ := http.NewRequest("POST", server.URL+"/deliveries", bytes.NewReader(commandBody))
			denied.Header = bearer(peer)
			deniedResponse, err := server.Client().Do(denied)
			if err != nil {
				t.Fatal(err)
			}
			deniedResponse.Body.Close()
			if deniedResponse.StatusCode != 401 {
				t.Fatal("store peer accepted as delivery producer", deniedResponse.StatusCode)
			}
			consume(commandBody)
			consume(commandBody)
			count, err = app.Store.Count(ctx, root)
			if err != nil || count != 1 {
				t.Fatal("redelivery duplicated effect", count, err)
			}
			if err = app.Store.Deliver(ctx, app.Publisher, 100); err != nil {
				t.Fatal(err)
			}
			publication := mailbox.pop()
			if publication == nil {
				t.Fatal("missing durable publication")
			}
			consume(publication)
			consume(publication)
			if _, err = client.Do(ctx, "GET", "/view-stores/documents/queued", nil, nil, bearer(session.Token)); err != nil {
				t.Fatal("queued projection", err)
			}
			_, err = client.Do(ctx, "POST", "/projections/documents/replay", nil, map[string]any{"runId": "rebuild-1", "limit": 10}, bearer(peer))
			if err != nil {
				t.Fatal("replay scheduling", err)
			}
			replayBody := mailbox.pop()
			if replayBody == nil {
				t.Fatal("no replay dispatch")
			}
			consume(replayBody)
			// Same logical run is deduplicated; a new run must produce new work.
			for _, run := range []string{"rebuild-1", "rebuild-2"} {
				_, err := client.Do(ctx, "POST", "/projections/documents/replay", nil, map[string]any{"runId": run, "limit": 10}, bearer(peer))
				if err != nil {
					t.Fatal(err)
				}
				body := mailbox.pop()
				if run == "rebuild-1" && body != nil {
					t.Fatal("replay retry produced another task")
				}
				if run == "rebuild-2" {
					if body == nil {
						t.Fatal("new replay was suppressed by old run")
					}
					consume(body)
				}
			}
			envelope, err := delivery.Decode(commandBody)
			if err != nil {
				t.Fatal(err)
			}
			// A changed retry becomes a durable terminal rejection; it must not
			// prevent the following valid command from committing.
			poisonInput := input
			poisonInput.Payload = json.RawMessage(`{"text":"changed retry"}`)
			poisonResponse, err := client.Do(ctx, "POST", "/commands/set", nil, poisonInput, bearer(session.Token))
			if err != nil {
				t.Fatal(err)
			}
			poisonAccepted := decodeCommand(t, poisonResponse)
			if poisonAccepted.Acceptance == nil || poisonAccepted.Acceptance.EnvelopeID == "" {
				t.Fatal("queued response omitted inspection identity")
			}
			poisonBody := mailbox.pop()
			if poisonBody == nil {
				t.Fatal("missing changed retry dispatch")
			}
			poisonEnvelope, err := delivery.Decode(poisonBody)
			if err != nil || poisonEnvelope.ID != poisonAccepted.Acceptance.EnvelopeID {
				t.Fatal("inspection ID is not the delivered envelope", err)
			}
			consume(poisonBody)
			if observed.Load() != 1 {
				t.Fatal("HTTP composition omitted rejection observer")
			}
			consume(poisonBody)
			if observed.Load() != 1 {
				t.Fatal("duplicate rejection repeated observer")
			}
			terminalResponse, err := client.Do(ctx, "GET", "/delivery-rejections/"+poisonAccepted.Acceptance.EnvelopeID, nil, nil, bearer(peer))
			if err != nil {
				t.Fatal("submitted ID not inspectable", err)
			}
			var terminal delivery.Rejection
			if eventstore.Decode(terminalResponse.Body, &terminal) != nil || terminal.Reason != delivery.RejectionIdempotency || terminal.EnvelopeID != poisonAccepted.Acceptance.EnvelopeID {
				t.Fatal("poison not recorded before acknowledgment", terminal)
			}
			next := input
			next.ID = "async-2"
			nextRoot := root
			nextRoot.ID = "following"
			next.Root = &nextRoot
			if _, err := client.Do(ctx, "POST", "/commands/set", nil, next, bearer(session.Token)); err != nil {
				t.Fatal(err)
			}
			nextBody := mailbox.pop()
			if nextBody == nil {
				t.Fatal("following command not dispatched")
			}
			consume(nextBody)
			if n, err := app.Store.Count(ctx, nextRoot); err != nil || n != 1 {
				t.Fatal("poison blocked following command", n, err)
			}
			envelope.Command.Events[0].Root.Network = "foreign"
			envelope.ID = ""
			bad, err := delivery.Encode(envelope)
			if err != nil {
				t.Fatal(err)
			}
			rejected, err := app.Delivery.Consume(ctx, bad)
			if err != nil || rejected.Rejected == nil || rejected.Rejected.Reason != delivery.RejectionForbidden {
				t.Fatal("foreign scope not durably rejected", rejected, err)
			}
			lookup, err := client.Do(ctx, "GET", "/delivery-rejections/"+rejected.Rejected.EnvelopeID, nil, nil, bearer(peer))
			if err != nil || len(lookup.Body) == 0 {
				t.Fatal("rejection inspection", err)
			}
			if _, err := client.Do(ctx, "GET", "/delivery-rejections/"+rejected.Rejected.EnvelopeID, nil, nil, bearer(session.Token)); err == nil {
				t.Fatal("user accessed internal rejection")
			}

		})
	}
}
