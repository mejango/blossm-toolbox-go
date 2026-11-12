package gcp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	tasks "cloud.google.com/go/cloudtasks/apiv2"
	tp "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	kms "cloud.google.com/go/kms/apiv1"
	kp "cloud.google.com/go/kms/apiv1/kmspb"
	logging "cloud.google.com/go/logging/apiv2"
	lp "cloud.google.com/go/logging/apiv2/loggingpb"
	pubsub "cloud.google.com/go/pubsub/v2/apiv1"
	pp "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	scheduler "cloud.google.com/go/scheduler/apiv1"
	sp "cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"google.golang.org/api/option"
	mr "google.golang.org/genproto/googleapis/api/monitoredres"
	lt "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

// These are real GAPIC clients over serialized gRPC frames to an in-process
// server. No network dial, ADC, metadata lookup or cloud endpoint is involved.
func wire(t *testing.T, handle func(context.Context, string, any) (any, error)) []option.ClientOption {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer(grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		return handle(ctx, info.FullMethod, req)
	}))
	tp.RegisterCloudTasksServer(server, &tp.UnimplementedCloudTasksServer{})
	sp.RegisterCloudSchedulerServer(server, &sp.UnimplementedCloudSchedulerServer{})
	pp.RegisterPublisherServer(server, &pp.UnimplementedPublisherServer{})
	pp.RegisterSubscriberServer(server, &pp.UnimplementedSubscriberServer{})
	kp.RegisterKeyManagementServiceServer(server, &kp.UnimplementedKeyManagementServiceServer{})
	lp.RegisterLoggingServiceV2Server(server, &lp.UnimplementedLoggingServiceV2Server{})
	go server.Serve(listener)
	conn, err := grpc.NewClient("passthrough:///gcp-test", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }))
	must(t, err)
	t.Cleanup(func() { conn.Close(); server.Stop(); listener.Close() })
	return []option.ClientOption{option.WithGRPCConn(conn), option.WithoutAuthentication()}
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

const location = "projects/p/locations/us-central1"
const ring = location + "/keyRings/r"
const key = ring + "/cryptoKeys/k"
const version = key + "/cryptoKeyVersions/1"

func TestTasksSchedulerWire(t *testing.T) {
	ctx := context.Background()
	h := HTTPDispatch{URL: "https://service.example/path?q=1", ServiceAccount: "executor@p.iam.gserviceaccount.com", Data: map[string]any{"x": "y"}}
	opts := wire(t, func(_ context.Context, method string, v any) (any, error) {
		switch r := v.(type) {
		case *tp.CreateTaskRequest:
			if r.Parent != location+"/queues/q" || r.Task.Name != location+"/queues/q/tasks/task-id" || string(r.Task.GetHttpRequest().Body) != `{"x":"y"}` || r.Task.GetHttpRequest().GetOidcToken().Audience != "https://service.example" || r.Task.ScheduleTime.Nanos != 123000000 {
				t.Errorf("bad task: %v", r)
			}
			return r.Task, nil
		case *tp.CreateQueueRequest:
			if r.Queue.RateLimits.MaxDispatchesPerSecond != 2 || r.Queue.StackdriverLoggingConfig.SamplingRatio != 0.1 {
				t.Errorf("bad queue: %v", r)
			}
			return r.Queue, nil
		case *tp.UpdateQueueRequest:
			if strings.Join(r.UpdateMask.Paths, ",") != "rate_limits.max_dispatches_per_second,rate_limits.max_concurrent_dispatches" || r.Queue.RateLimits.MaxConcurrentDispatches != 3 {
				t.Errorf("bad rate mask %v", r)
			}
			return r.Queue, nil
		case *tp.GetQueueRequest:
			return nil, status.Error(codes.NotFound, "absent")
		case *sp.CreateJobRequest:
			if r.Parent != location || string(r.Job.GetHttpTarget().Body) != `{"x":"y"}` || r.Job.GetHttpTarget().GetOidcToken().Audience != "https://service.example" {
				t.Errorf("bad job %v", r)
			}
			return r.Job, nil
		case *sp.UpdateJobRequest:
			if strings.Join(r.UpdateMask.Paths, ",") != "schedule,time_zone,http_target" {
				t.Errorf("bad job mask %v", r)
			}
			return r.Job, nil
		case *sp.GetJobRequest:
			return nil, status.Error(codes.PermissionDenied, "denied")
		case *sp.DeleteJobRequest:
			return nil, status.Error(codes.NotFound, "absent")
		}
		t.Errorf("unexpected %s", method)
		return nil, status.Error(codes.Unimplemented, "unexpected")
	})
	tc, err := tasks.NewClient(ctx, opts...)
	must(t, err)
	defer tc.Close()
	sc, err := scheduler.NewCloudSchedulerClient(ctx, opts...)
	must(t, err)
	defer sc.Close()
	task := Tasks{tc}
	_, err = task.Enqueue(ctx, location+"/queues/q", location+"/queues/q/tasks/task-id", h, time.Unix(100, 123000000))
	must(t, err)
	_, err = task.CreateQueue(ctx, location, location+"/queues/q", 2, 3)
	must(t, err)
	_, err = task.UpdateRates(ctx, location+"/queues/q", 2, 3)
	must(t, err)
	found, err := task.QueueExists(ctx, location+"/queues/q")
	must(t, err)
	if found {
		t.Fatal("unexpected queue")
	}
	sch := Scheduler{sc}
	spec := Schedule{Name: location + "/jobs/j", Cron: "0 * * * *", TimeZone: "UTC", Target: h}
	_, err = sch.Create(ctx, location, spec)
	must(t, err)
	_, err = sch.Update(ctx, spec)
	must(t, err)
	_, err = sch.Exists(ctx, spec.Name)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	must(t, sch.Delete(ctx, spec.Name))
	for _, method := range []string{"DELETE", "GET", "HEAD", "OPTIONS"} {
		h.Method = method
		if _, err = task.Enqueue(ctx, location+"/queues/q", "", h, time.Time{}); err == nil {
			t.Fatalf("Tasks accepted %s body", method)
		}
		spec.Target = h
		if _, err = sch.Create(ctx, location, spec); err == nil {
			t.Fatalf("Scheduler accepted %s body", method)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = task.Enqueue(canceled, "q", "", h, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
func TestPubSubWire(t *testing.T) {
	ctx := context.Background()
	payload := []byte{0, 255, ' ', '\n'}
	acks := make(chan string, 5)
	opts := wire(t, func(_ context.Context, method string, v any) (any, error) {
		switch r := v.(type) {
		case *pp.PublishRequest:
			if !bytes.Equal(r.Messages[0].Data, payload) || r.Messages[0].Attributes["id"] != "stable" {
				t.Errorf("bad publish %v", r)
			}
			return &pp.PublishResponse{MessageIds: []string{"accepted"}}, nil
		case *pp.Subscription:
			if r.AckDeadlineSeconds != 60 || r.ExpirationPolicy == nil || r.ExpirationPolicy.Ttl != nil || r.PushConfig.GetOidcToken().Audience != "https://service.example" {
				t.Errorf("bad subscription %v", r)
			}
			return r, nil
		case *pp.PullRequest:
			return &pp.PullResponse{ReceivedMessages: []*pp.ReceivedMessage{{AckId: "a", Message: &pp.PubsubMessage{Data: payload}}}}, nil
		case *pp.AcknowledgeRequest:
			acks <- "ack:" + r.AckIds[0]
			return &emptypb.Empty{}, nil
		case *pp.ModifyAckDeadlineRequest:
			if r.AckDeadlineSeconds != 0 && r.AckDeadlineSeconds != 120 {
				t.Errorf("bad deadline %v", r)
			}
			acks <- "deadline"
			return &emptypb.Empty{}, nil
		case *pp.Topic:
			return nil, status.Error(codes.AlreadyExists, "exists")
		case *pp.GetTopicRequest:
			return nil, status.Error(codes.PermissionDenied, "denied")
		case *pp.GetSubscriptionRequest:
			return nil, status.Error(codes.NotFound, "absent")
		case *pp.DeleteTopicRequest:
			return nil, status.Error(codes.NotFound, "absent")
		case *pp.DeleteSubscriptionRequest:
			return &emptypb.Empty{}, nil
		}
		t.Errorf("unexpected %s", method)
		return nil, status.Error(codes.Unimplemented, "unexpected")
	})
	pc, err := pubsub.NewTopicAdminClient(ctx, opts...)
	must(t, err)
	defer pc.Close()
	sc, err := pubsub.NewSubscriptionAdminClient(ctx, opts...)
	must(t, err)
	defer sc.Close()
	p := PubSub{pc, sc}
	id, err := p.PublishBytes(ctx, "projects/p/topics/t", payload, map[string]string{"id": "stable"})
	must(t, err)
	if id != "accepted" {
		t.Fatal(id)
	}
	push := HTTPDispatch{URL: "https://service.example", ServiceAccount: "push@p.iam.gserviceaccount.com"}
	_, err = p.CreateSubscription(ctx, "projects/p/subscriptions/s", "projects/p/topics/t", &push, nil)
	must(t, err)
	messages, err := p.Pull(ctx, "projects/p/subscriptions/s", 1)
	must(t, err)
	if !bytes.Equal(messages[0].Message.Data, payload) {
		t.Fatal(messages)
	}
	if len(acks) != 0 {
		t.Fatal("implicitly acknowledged")
	}
	must(t, p.Ack(ctx, "projects/p/subscriptions/s", messages[0].AckId))
	must(t, p.Nack(ctx, "projects/p/subscriptions/s", "a"))
	must(t, p.Extend(ctx, "projects/p/subscriptions/s", 120, "a"))
	if len(acks) != 3 {
		t.Fatal("missing acknowledgements")
	}
	if p.Ack(ctx, "projects/p/subscriptions/s", "") == nil || p.Extend(ctx, "projects/p/subscriptions/s", 601, "a") == nil {
		t.Fatal("invalid ack accepted")
	}
	push.Data = map[string]string{}
	if _, err = p.CreateSubscription(ctx, "projects/p/subscriptions/s", "t", &push, nil); err == nil {
		t.Fatal("ignored push data")
	}
	must(t, p.CreateTopic(ctx, "projects/p/topics/t"))
	must(t, p.DeleteTopic(ctx, "projects/p/topics/t"))
	must(t, p.DeleteSubscription(ctx, "projects/p/subscriptions/s"))
	_, err = p.TopicExists(ctx, "projects/p/topics/t")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	found, err := p.SubscriptionExists(ctx, "projects/p/subscriptions/s")
	must(t, err)
	if found {
		t.Fatal("absent subscription")
	}
}
func TestKMSWireCrypto(t *testing.T) {
	ctx := context.Background()
	private, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	must(t, err)
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	message := []byte{' ', 0, 255, '\n'}
	cipher := []byte("ciphertext")
	seen := make(chan string, 30)
	opts := wire(t, func(_ context.Context, _ string, v any) (any, error) {
		switch r := v.(type) {
		case *kp.EncryptRequest:
			if !bytes.Equal(r.Plaintext, message) || !validChecksum(r.Plaintext, r.PlaintextCrc32C) {
				t.Errorf("bad plaintext %v", r)
			}
			name := version
			if r.Name == "wrong" {
				name = ring + "/cryptoKeys/other/cryptoKeyVersions/1"
			}
			return &kp.EncryptResponse{Name: name, Ciphertext: cipher, CiphertextCrc32C: checksum(cipher), VerifiedPlaintextCrc32C: true}, nil
		case *kp.DecryptRequest:
			if !validChecksum(r.Ciphertext, r.CiphertextCrc32C) {
				t.Errorf("bad ciphertext checksum")
			}
			check := checksum(message)
			if r.Name == "corrupt" {
				check.Value++
			}
			return &kp.DecryptResponse{Plaintext: message, PlaintextCrc32C: check}, nil
		case *kp.AsymmetricSignRequest:
			d := sha256.Sum256(message)
			if !bytes.Equal(d[:], r.Digest.GetSha256()) || !validChecksum(d[:], r.DigestCrc32C) {
				t.Errorf("SHA256/checksum mismatch")
			}
			sig, e := ecdsa.SignASN1(rand.Reader, private, r.Digest.GetSha256())
			if e != nil {
				return nil, e
			}
			return &kp.AsymmetricSignResponse{Name: r.Name, Signature: sig, SignatureCrc32C: checksum(sig), VerifiedDigestCrc32C: true}, nil
		case *kp.GetPublicKeyRequest:
			seen <- r.Name
			return &kp.PublicKey{Name: r.Name, Pem: public, PemCrc32C: checksum([]byte(public)), Algorithm: kp.CryptoKeyVersion_EC_SIGN_P256_SHA256}, nil
		case *kp.CreateCryptoKeyRequest:
			if r.CryptoKey.Purpose == kp.CryptoKey_ASYMMETRIC_SIGN && r.CryptoKey.VersionTemplate.Algorithm != kp.CryptoKeyVersion_EC_SIGN_P256_SHA256 {
				t.Errorf("bad signing key")
			}
			return r.CryptoKey, nil
		case *kp.GetCryptoKeyRequest:
			return nil, status.Error(codes.NotFound, "absent")
		case *kp.CreateKeyRingRequest:
			return r.KeyRing, nil
		case *kp.GetKeyRingRequest:
			return nil, status.Error(codes.PermissionDenied, "denied")
		}
		return nil, status.Error(codes.Unimplemented, "unexpected")
	})
	client, err := kms.NewKeyManagementClient(ctx, opts...)
	must(t, err)
	defer client.Close()
	k := KMS{client}
	out, err := k.Encrypt(ctx, key, message)
	must(t, err)
	plain, err := k.Decrypt(ctx, key, out)
	must(t, err)
	if !bytes.Equal(message, plain) {
		t.Fatal("lossy plaintext")
	}
	_, err = k.Encrypt(ctx, "wrong", message)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatal("wrong key accepted", err)
	}
	_, err = k.Decrypt(ctx, "corrupt", cipher)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatal("corruption accepted", err)
	}
	sig, err := k.Sign(ctx, version, message)
	must(t, err)
	ok, err := k.Verify(ctx, version, message, sig)
	must(t, err)
	if !ok {
		t.Fatal("signature rejected")
	}
	for _, bad := range [][]byte{{1, 2, 3}, append(sig, 0), {}} {
		ok, err = k.Verify(ctx, version, message, bad)
		must(t, err)
		if ok {
			t.Fatal("malformed signature accepted")
		}
	}
	ok, err = k.Verify(ctx, version, []byte("tampered"), sig)
	must(t, err)
	if ok {
		t.Fatal("tamper accepted")
	}
	jose, err := k.SignES256(ctx, version, message)
	must(t, err)
	if len(jose) != 64 {
		t.Fatal("not JOSE64")
	}
	ok, err = k.VerifyES256(ctx, version, message, jose)
	must(t, err)
	if !ok {
		t.Fatal("JOSE rejected")
	}
	ok, err = k.VerifyES256(ctx, version, message, sig)
	must(t, err)
	if ok {
		t.Fatal("DER accepted as JOSE")
	}
	_, err = k.PublicKey(ctx, key+"/cryptoKeyVersions/2")
	must(t, err)
	close(seen)
	last := ""
	for n := range seen {
		last = n
	}
	if !strings.HasSuffix(last, "/2") {
		t.Fatal("cached wrong version")
	}
	if _, err = k.Sign(ctx, key+"/cryptoKeyVersions/latest", message); err == nil {
		t.Fatal("alias accepted")
	}
	must(t, k.CreateKey(ctx, ring, "sign", true, 0, time.Time{}))
	must(t, k.CreateKey(ctx, ring, "sym", false, 24*time.Hour, time.Now().Add(time.Hour)))
	must(t, k.CreateKeyRing(ctx, location, "r"))
	found, err := k.KeyExists(ctx, key)
	must(t, err)
	if found {
		t.Fatal("absent key")
	}
	_, err = k.KeyRingExists(ctx, ring)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
}
func TestLoggingWire(t *testing.T) {
	ctx := context.Background()
	opts := wire(t, func(_ context.Context, _ string, v any) (any, error) {
		r := v.(*lp.WriteLogEntriesRequest)
		if r.LogName != "projects/p/logs/app" || r.Resource.Type != "global" || r.Entries[0].GetJsonPayload().Fields["message"].GetStringValue() != "hello" || r.Entries[0].Severity != lt.LogSeverity_INFO || r.Labels["service"] != "test" {
			t.Errorf("bad log %v", r)
		}
		return &lp.WriteLogEntriesResponse{}, nil
	})
	c, err := logging.NewClient(ctx, opts...)
	must(t, err)
	defer c.Close()
	l := Logging{Client: c, LogName: "projects/p/logs/app", Resource: &mr.MonitoredResource{Type: "global"}, Labels: map[string]string{"service": "test"}}
	must(t, l.Write(ctx, lt.LogSeverity_INFO, "stable", time.Now(), map[string]any{"message": "hello"}))
}

func TestKMSIntegrityFailures(t *testing.T) {
	ctx := context.Background()
	for _, fault := range []string{"checksum", "unverified", "identity"} {
		t.Run(fault, func(t *testing.T) {
			opts := wire(t, func(_ context.Context, _ string, v any) (any, error) {
				switch r := v.(type) {
				case *kp.AsymmetricSignRequest:
					response := &kp.AsymmetricSignResponse{Name: r.Name, Signature: []byte("sig"), SignatureCrc32C: checksum([]byte("sig")), VerifiedDigestCrc32C: true}
					switch fault {
					case "checksum":
						response.SignatureCrc32C.Value++
					case "unverified":
						response.VerifiedDigestCrc32C = false
					case "identity":
						response.Name = key + "/cryptoKeyVersions/2"
					}
					return response, nil
				case *kp.GetPublicKeyRequest:
					response := &kp.PublicKey{Name: r.Name, Pem: "pem", PemCrc32C: checksum([]byte("pem"))}
					if fault == "identity" {
						response.Name = key + "/cryptoKeyVersions/2"
					} else {
						response.PemCrc32C = nil
					}
					return response, nil
				case *kp.EncryptRequest:
					response := &kp.EncryptResponse{Name: version, Ciphertext: []byte("cipher"), CiphertextCrc32C: checksum([]byte("cipher")), VerifiedPlaintextCrc32C: true}
					switch fault {
					case "checksum":
						response.CiphertextCrc32C.Value++
					case "unverified":
						response.VerifiedPlaintextCrc32C = false
					case "identity":
						response.Name = "projects/other/locations/global/keyRings/r/cryptoKeys/k/cryptoKeyVersions/1"
					}
					return response, nil
				}
				return nil, status.Error(codes.Unimplemented, "unexpected")
			})
			c, err := kms.NewKeyManagementClient(ctx, opts...)
			must(t, err)
			defer c.Close()
			k := KMS{c}
			if _, err = k.Sign(ctx, version, []byte("data")); !errors.Is(err, ErrIntegrity) {
				t.Fatal("signature integrity failure hidden", err)
			}
			if _, err = k.PublicKey(ctx, version); !errors.Is(err, ErrIntegrity) {
				t.Fatal("public key integrity failure hidden", err)
			}
			if _, err = k.Encrypt(ctx, key, []byte("data")); !errors.Is(err, ErrIntegrity) {
				t.Fatal("ciphertext integrity failure hidden", err)
			}
		})
	}
}
