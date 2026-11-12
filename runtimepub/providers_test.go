package runtimepub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pubsub "cloud.google.com/go/pubsub/v2/apiv1"
	pb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

func validNotice(t *testing.T) eventstore.Publication {
	t.Helper()
	input := eventstore.Input{Root: eventstore.Root{Network: "test", Service: "core", Domain: "session", ID: "root"}, Action: "started", Key: "stable", Created: "2026-11-12T00:00:00Z", Payload: json.RawMessage(`{"name":"session"}`), Context: json.RawMessage(`{}`), Transaction: eventstore.Transaction{ID: "tx", Issued: "2026-11-12T00:00:00Z", Path: []eventstore.Trace{}}}
	fingerprint, err := eventstore.Digest(input)
	if err != nil {
		t.Fatal(err)
	}
	event := eventstore.Event{Input: input, Number: 0, Fingerprint: fingerprint}
	event.Hash, err = eventstore.Digest(event)
	if err != nil {
		t.Fatal(err)
	}
	return eventstore.Publication{ID: event.Hash, Event: event}
}
func gcpClients(t *testing.T, fn func(context.Context, any) (any, error)) (*pubsub.TopicAdminClient, *pubsub.SubscriptionAdminClient) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, request any, _ *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		return fn(ctx, request)
	}))
	pb.RegisterPublisherServer(server, &pb.UnimplementedPublisherServer{})
	pb.RegisterSubscriberServer(server, &pb.UnimplementedSubscriberServer{})
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	connection := func() *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///runtime-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	// Production constructors own distinct connections. Sharing one here makes
	// the second client's Close report a double-close error unrelated to runtime.
	publisher, err := pubsub.NewTopicAdminClient(context.Background(), option.WithGRPCConn(connection()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	subscriber, err := pubsub.NewSubscriptionAdminClient(context.Background(), option.WithGRPCConn(connection()), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	return publisher, subscriber
}
func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime did not finish")
	}
}

func TestGCPRuntimeWire(t *testing.T) {
	for _, scenario := range []string{"success", "handler-error", "handler-deadline", "tamper", "id-mismatch", "metadata-mismatch", "metadata-empty", "ack-error", "lease-error"} {
		t.Run(scenario, func(t *testing.T) {
			notice := validNotice(t)
			incoming := notice
			switch scenario {
			case "tamper":
				incoming.Event.Input.Payload = json.RawMessage(`{"name":"tampered"}`)
			case "id-mismatch":
				incoming.ID = "wrong"
			}
			body, err := eventstore.Encode(incoming)
			if err != nil {
				t.Fatal(err)
			}
			var received, published, handled, acked, nacked, leases atomic.Int32
			processed := make(chan struct{})
			p, s := gcpClients(t, func(ctx context.Context, request any) (any, error) {
				switch r := request.(type) {
				case *pb.PublishRequest:
					published.Add(1)
					if r.Topic != "projects/p/topics/events" || len(r.Messages) != 1 {
						t.Error("invalid publish target")
					}
					msg := r.Messages[0]
					n, e := decodeNotice(msg.Data, msg.Attributes["eventId"])
					if e != nil || n.ID != notice.ID {
						t.Error("invalid outgoing notice", e)
					}
					return &pb.PublishResponse{MessageIds: []string{"accepted"}}, nil
				case *pb.PullRequest:
					if r.MaxMessages != 1 || r.Subscription != "projects/p/subscriptions/events" {
						t.Error("not bounded single receive")
					}
					if received.Add(1) > 1 {
						close(processed)
						<-ctx.Done()
						return nil, status.FromContextError(ctx.Err()).Err()
					}
					id := notice.ID
					if scenario == "metadata-mismatch" {
						id = "wrong"
					}
					if scenario == "metadata-empty" {
						id = ""
					}
					return &pb.PullResponse{ReceivedMessages: []*pb.ReceivedMessage{{AckId: "receipt-original", Message: &pb.PubsubMessage{Data: body, Attributes: map[string]string{"eventId": id}}}}}, nil
				case *pb.ModifyAckDeadlineRequest:
					if len(r.AckIds) != 1 || r.AckIds[0] != "receipt-original" {
						t.Error("wrong receipt")
					}
					if r.AckDeadlineSeconds == 60 {
						leases.Add(1)
						if scenario == "lease-error" {
							return nil, status.Error(codes.PermissionDenied, "denied")
						}
					} else if r.AckDeadlineSeconds == 0 {
						nacked.Add(1)
					} else {
						t.Error("unexpected lease")
					}
					return &emptypb.Empty{}, nil
				case *pb.AcknowledgeRequest:
					acked.Add(1)
					if len(r.AckIds) != 1 || r.AckIds[0] != "receipt-original" {
						t.Error("wrong ack receipt")
					}
					if scenario == "ack-error" {
						return nil, status.Error(codes.PermissionDenied, "ack failure")
					}
					return &emptypb.Empty{}, nil
				}
				return nil, status.Error(codes.Unimplemented, "unexpected")
			})
			runtime, err := NewGCP(p, s, "projects/p/topics/events", "projects/p/subscriptions/events", Options{HandleTimeout: 20 * time.Millisecond, PullTimeout: time.Second, AckTimeout: time.Second, RetryDelay: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			if err = runtime.Publish(context.Background(), notice); err != nil {
				t.Fatal(err)
			}
			broken := notice
			broken.Event.Input.Payload = json.RawMessage(`{}`)
			if runtime.Publish(context.Background(), broken) == nil {
				t.Fatal("published tampered immutable event")
			}
			if published.Load() != 1 {
				t.Fatal("invalid notice reached SDK")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				runtime.Consume(ctx, publishFunc(func(ctx context.Context, n eventstore.Publication) error {
					handled.Add(1)
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 25*time.Millisecond {
						t.Error("unbounded handler")
					}
					if n.ID != notice.ID {
						t.Error("wrong delivered identity")
					}
					if scenario == "handler-error" {
						return errors.New("handler failed")
					}
					if scenario == "handler-deadline" {
						<-ctx.Done()
						return nil
					}
					return nil
				}), slog.New(slog.NewTextHandler(io.Discard, nil)))
			}()
			await(t, processed)
			cancel()
			await(t, finished)
			if leases.Load() != 1 {
				t.Fatal("missing lease extension")
			}
			wantHandle := int32(1)
			if scenario == "tamper" || scenario == "id-mismatch" || scenario == "metadata-mismatch" || scenario == "metadata-empty" || scenario == "lease-error" {
				wantHandle = 0
			}
			if handled.Load() != wantHandle {
				t.Fatalf("handled %d want %d", handled.Load(), wantHandle)
			}
			wantAck := int32(0)
			if scenario == "success" || scenario == "ack-error" {
				wantAck = 1
			}
			if acked.Load() != wantAck {
				t.Fatalf("acknowledged failed work: %d", acked.Load())
			}
			wantNack := int32(0)
			if wantAck == 0 && scenario != "lease-error" {
				wantNack = 1
			}
			if nacked.Load() != wantNack {
				t.Fatalf("nack = %d want %d", nacked.Load(), wantNack)
			}
		})
	}
}

type closingPublisher struct {
	GCPPublisher
	closed *atomic.Int32
}

func (p closingPublisher) Close() error { p.closed.Add(1); return p.GCPPublisher.Close() }

type closingSubscriber struct {
	GCPSubscriber
	closed *atomic.Int32
}

func (s closingSubscriber) Close() error { s.closed.Add(1); return s.GCPSubscriber.Close() }

func TestRuntimeCloseJoinsHandlerBeforeClosingClients(t *testing.T) {
	notice := validNotice(t)
	body, _ := eventstore.Encode(notice)
	var closed atomic.Int32
	p, s := gcpClients(t, func(ctx context.Context, request any) (any, error) {
		switch request.(type) {
		case *pb.PullRequest:
			return &pb.PullResponse{ReceivedMessages: []*pb.ReceivedMessage{{AckId: "receipt", Message: &pb.PubsubMessage{Data: body}}}}, nil
		case *pb.ModifyAckDeadlineRequest:
			return &emptypb.Empty{}, nil
		}
		return nil, status.Error(codes.Unimplemented, "unexpected")
	})
	runtime, err := NewGCP(closingPublisher{p, &closed}, closingSubscriber{s, &closed}, "projects/p/topics/events", "projects/p/subscriptions/events", Options{})
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		runtime.Consume(context.Background(), publishFunc(func(ctx context.Context, _ eventstore.Publication) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return ctx.Err()
		}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	await(t, entered)
	closing := make(chan struct{})
	go func() {
		defer close(closing)
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	}()
	await(t, canceled)
	if closed.Load() != 0 {
		t.Fatal("clients closed before handler stopped")
	}
	select {
	case <-closing:
		t.Fatal("close did not join handler")
	default:
	}
	close(release)
	await(t, finished)
	await(t, closing)
	if closed.Load() != 2 {
		t.Fatal("clients not closed exactly once")
	}
	if err = runtime.Close(); err != nil || closed.Load() != 2 {
		t.Fatal("non-idempotent close")
	}
	if !errors.Is(runtime.Publish(context.Background(), notice), ErrClosed) {
		t.Fatal("publish allowed after close")
	}
	runtime.Consume(context.Background(), publishFunc(func(context.Context, eventstore.Publication) error { t.Error("handler after close"); return nil }), nil)
}

func TestNoticeBoundariesAndOptions(t *testing.T) {
	notice := validNotice(t)
	body, err := eventstore.Encode(notice)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeNotice(append(body, []byte(` {}`)...), ""); err == nil {
		t.Fatal("trailing object accepted")
	}
	if _, err = decodeNotice([]byte(strings.Replace(string(body), `"attempts":0`, `"attempts":0,"attempts":0`, 1)), ""); err == nil {
		t.Fatal("duplicate field accepted")
	}
	for _, opts := range []Options{{HandleTimeout: -1}, {PullTimeout: -1}, {AckTimeout: -1}, {RetryDelay: -1}, {HandleTimeout: 59 * time.Second, AckTimeout: time.Second}} {
		if _, err := opts.defaults(); err == nil {
			t.Fatal("invalid timeout accepted", opts)
		}
	}
}

func sizedNotice(t *testing.T, size int) eventstore.Publication {
	t.Helper()
	n := validNotice(t)
	n.Attempts = 123456
	n.Event.Input.Payload = json.RawMessage(`{"data":""}`)
	rehash := func() {
		var err error
		n.Event.Fingerprint, err = eventstore.Digest(n.Event.Input)
		if err != nil {
			t.Fatal(err)
		}
		n.Event.Hash = ""
		n.Event.Hash, err = eventstore.Digest(n.Event)
		if err != nil {
			t.Fatal(err)
		}
		n.ID = n.Event.Hash
	}
	rehash()
	base, err := eventstore.Encode(n)
	if err != nil {
		t.Fatal(err)
	}
	if size < len(base) {
		t.Fatal("invalid test size")
	}
	n.Event.Input.Payload = json.RawMessage(`{"data":"` + strings.Repeat("x", size-len(base)) + `"}`)
	rehash()
	wire, err := eventstore.Encode(n)
	if err != nil || len(wire) != size || eventstore.Verify(n.Event) != nil {
		t.Fatal("invalid sized fixture", len(wire), err)
	}
	return n
}

func checkPublishSize(t *testing.T, r *Runtime, calls *atomic.Int32) {
	t.Helper()
	if err := r.Publish(context.Background(), sizedNotice(t, eventstore.MaxPublicationBytes+1)); !errors.Is(err, eventstore.ErrInvalid) {
		t.Fatal("oversize notice did not return ErrInvalid", err)
	}
	if calls.Load() != 0 {
		t.Fatal("oversize notice reached SDK endpoint")
	}
	if err := r.Publish(context.Background(), sizedNotice(t, eventstore.MaxPublicationBytes)); err != nil {
		t.Fatal("boundary rejected", err)
	}
	if calls.Load() != 1 {
		t.Fatal("boundary not published", calls.Load())
	}
}
func TestGCPPublishSizePreflight(t *testing.T) {
	var calls atomic.Int32
	p, s := gcpClients(t, func(ctx context.Context, req any) (any, error) {
		calls.Add(1)
		request, ok := req.(*pb.PublishRequest)
		if !ok {
			t.Error("unexpected SDK method")
			return nil, errors.New("unexpected SDK method")
		}
		msg := request.Messages[0]
		var notice eventstore.Publication
		if err := eventstore.Decode(msg.Data, &notice); err != nil || notice.Attempts != 123456 || len(msg.Data) != eventstore.MaxPublicationBytes {
			t.Error("wire size or Attempts changed", err)
		}
		return &pb.PublishResponse{MessageIds: []string{"accepted"}}, nil
	})
	r, err := NewGCP(p, s, "projects/p/topics/events", "projects/p/subscriptions/events", Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	}()
	checkPublishSize(t, r, &calls)
}
