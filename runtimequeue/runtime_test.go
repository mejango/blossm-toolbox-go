package runtimequeue

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"

	taskspb "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/googleapis/gax-go/v2"
	cloudaws "github.com/mejango/blossm-toolbox-go/cloud/aws"
	"github.com/mejango/blossm-toolbox-go/cloud/gcp"
	"github.com/mejango/blossm-toolbox-go/command"
	"github.com/mejango/blossm-toolbox-go/delivery"
	"github.com/mejango/blossm-toolbox-go/eventstore"
	"github.com/mejango/blossm-toolbox-go/projection"
	"google.golang.org/api/idtoken"
	"google.golang.org/api/option"
)

type fakeTasks struct {
	gcp.TasksAPI
	requests []*taskspb.CreateTaskRequest
}

func (f *fakeTasks) CreateTask(_ context.Context, in *taskspb.CreateTaskRequest, _ ...gax.CallOption) (*taskspb.Task, error) {
	f.requests = append(f.requests, in)
	return in.Task, nil
}

type fakeSQS struct {
	cloudaws.SQSAPI
	sent    []*sqs.SendMessageInput
	receive func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error)
	ack     func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error)
}

func (f *fakeSQS) SendMessage(_ context.Context, in *sqs.SendMessageInput, _ ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	f.sent = append(f.sent, in)
	return &sqs.SendMessageOutput{MessageId: sdk.String("accepted")}, nil
}
func (f *fakeSQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return f.receive(ctx, in)
}
func (f *fakeSQS) DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return f.ack(ctx, in)
}
func gcpEnv() map[string]string {
	return map[string]string{"DELIVERY_TASKS_QUEUE": "projects/test/locations/us/queues/work", "DELIVERY_URL": "https://example.test/deliveries", "DELIVERY_SERVICE_ACCOUNT": "worker@test.iam.gserviceaccount.com", "DELIVERY_AUDIENCE": "https://example.test"}
}
func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
func sampleSubmission() command.Submission {
	return command.Submission{ID: "command", Events: []eventstore.Input{{Root: eventstore.Root{Network: "network", Service: "service", Domain: "document", ID: "root"}, Action: "set", Key: "key", Payload: json.RawMessage(`{"value":1}`)}}}
}
func validPayload() *idtoken.Payload {
	return &idtoken.Payload{Issuer: "https://accounts.google.com", Audience: "https://example.test", Expires: time.Now().Add(time.Hour).Unix(), Subject: "google-service-account-id", Claims: map[string]any{"email": "worker@test.iam.gserviceaccount.com", "email_verified": true}}
}

func TestConfigurationBeforeClients(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			f := factories{tasks: func(context.Context) (gcp.TasksAPI, func() error, error) { calls++; return &fakeTasks{}, nil, nil }, sqs: func(context.Context) (cloudaws.SQSAPI, error) { calls++; return &fakeSQS{}, nil }, validate: idtoken.Validate}
			if _, err := open(context.Background(), provider, env(map[string]string{}), f); err == nil || calls != 0 {
				t.Fatalf("missing config discovered credentials: %v", err)
			}
		})
	}
	for _, change := range []struct{ key, value string }{{"DELIVERY_URL", "http://example.test/deliveries"}, {"DELIVERY_URL", "https://user:pass@example.test/deliveries"}, {"DELIVERY_TASKS_QUEUE", "queues/work"}, {"DELIVERY_SERVICE_ACCOUNT", "outsider@example.com"}, {"DELIVERY_AUDIENCE", ""}} {
		m := gcpEnv()
		m[change.key] = change.value
		if _, err := open(context.Background(), "gcp", env(m), factories{}); err == nil {
			t.Fatalf("accepted %s", change.key)
		}
	}
	for _, m := range []map[string]string{
		{"DELIVERY_QUEUE_URL": "https://sqs.test/1/queue.fifo"},
		{"DELIVERY_QUEUE_URL": "https://sqs.test/1/queue", "DELIVERY_GROUP_ID": "wrong"},
		{"DELIVERY_QUEUE_URL": "https://sqs.test/1/queue?query=1"},
		{"DELIVERY_QUEUE_URL": "https://sqs.test/1/queue", "DELIVERY_TIMEOUT": "0s"},
		{"DELIVERY_QUEUE_URL": "https://sqs.test/1/queue", "DELIVERY_TIMEOUT": "12h"},
	} {
		if _, err := open(context.Background(), "aws", env(m), factories{}); err == nil {
			t.Fatal("invalid SQS configuration accepted")
		}
	}
	if _, err := Open(context.Background(), "unknown"); err == nil {
		t.Fatal("unknown provider accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, "gcp"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestTasksCompositionAndCleanup(t *testing.T) {
	client := &fakeTasks{}
	closed := 0
	f := factories{tasks: func(context.Context) (gcp.TasksAPI, func() error, error) {
		return client, func() error { closed++; return nil }, nil
	}, validate: func(context.Context, string, string) (*idtoken.Payload, error) { return validPayload(), nil }}
	r, err := open(context.Background(), "gcp", env(gcpEnv()), f)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 0 {
		t.Fatal("Open started work")
	}
	if _, err = r.Commands.Enqueue(context.Background(), sampleSubmission()); err != nil {
		t.Fatal(err)
	}
	request := client.requests[0]
	dispatch := request.Task.GetHttpRequest()
	oidc := dispatch.GetOidcToken()
	if request.Parent != gcpEnv()["DELIVERY_TASKS_QUEUE"] || dispatch.Url != gcpEnv()["DELIVERY_URL"] || oidc == nil || oidc.Audience != gcpEnv()["DELIVERY_AUDIENCE"] || oidc.ServiceAccountEmail != gcpEnv()["DELIVERY_SERVICE_ACCOUNT"] {
		t.Fatalf("dispatch: %+v", dispatch)
	}
	envelope, err := delivery.Decode(dispatch.Body)
	if err != nil || envelope.Command.ID != "command" {
		t.Fatalf("envelope: %v", err)
	}
	if _, err = r.DeliveryVerifier(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
	if err = r.Run(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	_ = r.Close()
	if closed != 1 {
		t.Fatalf("closed%d times", closed)
	}
	f.tasks = func(context.Context) (gcp.TasksAPI, func() error, error) {
		return nil, func() error { closed++; return nil }, nil
	}
	if _, err = open(context.Background(), "gcp", env(gcpEnv()), f); err == nil || closed != 2 {
		t.Fatalf("construction cleanup: %v", err)
	}
}

func TestReplayIdentityAndFIFO(t *testing.T) {
	client := &fakeSQS{}
	m := map[string]string{"DELIVERY_QUEUE_URL": "https://sqs.test/1/work.fifo", "DELIVERY_GROUP_ID": "worker"}
	r, err := open(context.Background(), "aws", env(m), factories{sqs: func(context.Context) (cloudaws.SQSAPI, error) { return client, nil }})
	if err != nil {
		t.Fatal(err)
	}
	input := projection.Request{Root: sampleSubmission().Events[0].Root}
	if err = r.Replay.Enqueue(context.Background(), input); err == nil || len(client.sent) != 0 {
		t.Fatal("missing run reached provider", err)
	}
	for _, run := range []projection.RunID{"run-1", "run-1", "run-2"} {
		input.RunID = run
		if err = r.Replay.Enqueue(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}
	envelopes := make([]delivery.Envelope, 3)
	for i, sent := range client.sent {
		envelopes[i], err = delivery.Decode([]byte(*sent.MessageBody))
		if err != nil {
			t.Fatal(err)
		}
		if *sent.MessageGroupId != "worker" || *sent.MessageDeduplicationId != envelopes[i].ID {
			t.Fatal("FIFO identity missing")
		}
	}
	if envelopes[0].ID != envelopes[1].ID || envelopes[0].ID == envelopes[2].ID || envelopes[0].Replay.RunID != "run-1" || envelopes[2].Replay.RunID != "run-2" {
		t.Fatal("logical run identity lost", envelopes)
	}
	if _, err = r.DeliveryVerifier(context.Background(), "anything"); !errors.Is(err, ErrDeliveryUnauthorized) {
		t.Fatal("AWS enabled HTTP delivery")
	}
}

type appendFn func(context.Context, []eventstore.Input) ([]eventstore.Receipt, error)

func (f appendFn) Append(ctx context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
	return f(ctx, in)
}
func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
func workerRuntime(t *testing.T, client *fakeSQS) *Runtime {
	t.Helper()
	r, err := open(context.Background(), "aws", env(map[string]string{"DELIVERY_QUEUE_URL": "https://sqs.test/1/work"}), factories{sqs: func(context.Context) (cloudaws.SQSAPI, error) { return client, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestWorkerCommitBeforeAckAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	submission := sampleSubmission()
	body, err := delivery.Encode(delivery.Envelope{Command: &submission})
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	acked := false
	client := &fakeSQS{}
	client.receive = func(ctx context.Context, in *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
		if _, ok := ctx.Deadline(); !ok || in.MaxNumberOfMessages != 1 || in.VisibilityTimeout != 90 || in.WaitTimeSeconds != 20 {
			t.Fatal("unbounded worker")
		}
		return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{{Body: sdk.String(string(body)), ReceiptHandle: sdk.String("receipt")}}}, nil
	}
	client.ack = func(_ context.Context, in *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
		if !committed || *in.ReceiptHandle != "receipt" {
			t.Fatal("acked before commit")
		}
		acked = true
		cancel()
		return &sqs.DeleteMessageOutput{}, nil
	}
	consumer := &delivery.Consumer{Admit: func(context.Context, delivery.Envelope) error { return nil }, Commands: appendFn(func(_ context.Context, in []eventstore.Input) ([]eventstore.Receipt, error) {
		committed = true
		return []eventstore.Receipt{{Outcome: eventstore.Committed, Event: eventstore.Event{Input: in[0]}}}, nil
	})}
	r := workerRuntime(t, client)
	if err = r.Run(ctx, consumer, quietLogger()); !errors.Is(err, context.Canceled) || !acked {
		t.Fatalf("worker: %v acked%v", err, acked)
	}
}
func TestWorkerFailureBackoffAndDeadlineNoAck(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "receive-failure", true: "consume-timeout"}[timeout], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			client := &fakeSQS{}
			acks := 0
			client.ack = func(context.Context, *sqs.DeleteMessageInput) (*sqs.DeleteMessageOutput, error) {
				acks++
				return nil, errors.New("unexpected ack")
			}
			submission := sampleSubmission()
			body, err := delivery.Encode(delivery.Envelope{Command: &submission})
			if err != nil {
				t.Fatal(err)
			}
			client.receive = func(context.Context, *sqs.ReceiveMessageInput) (*sqs.ReceiveMessageOutput, error) {
				if !timeout {
					return nil, errors.New("provider https://user:secret-canary@queue.invalid offline")
				}
				return &sqs.ReceiveMessageOutput{Messages: []sqstypes.Message{{Body: sdk.String(string(body)), ReceiptHandle: sdk.String("r")}}}, nil
			}
			consumer := &delivery.Consumer{Admit: func(context.Context, delivery.Envelope) error { return nil }, Commands: appendFn(func(ctx context.Context, _ []eventstore.Input) ([]eventstore.Receipt, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			})}
			r := workerRuntime(t, client)
			r.timeout = 2 * time.Millisecond
			var delays []time.Duration
			r.wait = func(ctx context.Context, d time.Duration) error {
				delays = append(delays, d)
				if len(delays) == 7 {
					cancel()
					return ctx.Err()
				}
				return nil
			}
			var logs strings.Builder
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			if err = r.Run(ctx, consumer, logger); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if strings.Contains(logs.String(), "secret-canary") || strings.Contains(logs.String(), "queue.invalid") || !strings.Contains(logs.String(), "delivery_failure") {
				t.Fatalf("unsafe or missing failure log: %s", logs.String())
			}
			want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
			if acks != 0 || len(delays) != len(want) {
				t.Fatal("failed work acknowledged or retries missing")
			}
			for i, d := range delays {
				if d != want[i]*time.Second {
					t.Fatalf("backoff%d:%s", i, d)
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal("backoff ignores shutdown")
	}
}

func TestGoogleIdentityRestrictions(t *testing.T) {
	for _, field := range []string{"issuer", "audience", "email", "verified", "subject", "expiry", "validator"} {
		t.Run(field, func(t *testing.T) {
			p := validPayload()
			switch field {
			case "issuer":
				p.Issuer = "https://evil.test"
			case "audience":
				p.Audience = "other"
			case "email":
				p.Claims["email"] = "other@test.iam.gserviceaccount.com"
			case "verified":
				p.Claims["email_verified"] = "true"
			case "subject":
				p.Subject = ""
			case "expiry":
				p.Expires = 1
			}
			verify := googleVerifier("https://example.test", "worker@test.iam.gserviceaccount.com", func(ctx context.Context, _ string, aud string) (*idtoken.Payload, error) {
				if _, ok := ctx.Deadline(); !ok || aud != "https://example.test" {
					t.Fatal("validator not bounded/audience-bound")
				}
				if field == "validator" {
					return nil, errors.New("invalid signature")
				}
				return p, nil
			})
			if _, err := verify(context.Background(), "token"); !errors.Is(err, ErrDeliveryUnauthorized) {
				t.Fatalf("accepted %s: %v", field, err)
			}
		})
	}
}

type roundTripFn func(*http.Request) (*http.Response, error)

func (f roundTripFn) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGoogleOfficialSignatureVerificationOffline(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	certs, _ := json.Marshal(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "test", "alg": "RS256", "n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes())}}})
	client := &http.Client{Transport: roundTripFn(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "www.googleapis.com" {
			t.Fatalf("unexpected certificate host %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Cache-Control": []string{"max-age=3600"}}, Body: io.NopCloser(strings.NewReader(string(certs)))}, nil
	})}
	validator, err := idtoken.NewValidator(context.Background(), option.WithHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	p := validPayload()
	claims := map[string]any{"iss": p.Issuer, "aud": p.Audience, "sub": p.Subject, "exp": p.Expires, "email": p.Claims["email"], "email_verified": true}
	body, _ := json.Marshal(claims)
	unsigned := enc([]byte(`{"alg":"RS256","kid":"test","typ":"JWT"}`)) + "." + enc(body)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	token := unsigned + "." + enc(signature)
	verify := googleVerifier(p.Audience, p.Claims["email"].(string), validator.Validate)
	if _, err = verify(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	signature[0] ^= 1
	if _, err = verify(context.Background(), unsigned+"."+enc(signature)); !errors.Is(err, ErrDeliveryUnauthorized) {
		t.Fatalf("forged signature accepted: %v", err)
	}
}

func TestTasksReplayExplicitRunIdentity(t *testing.T) {
	client := &fakeTasks{}
	r, err := open(context.Background(), "gcp", env(gcpEnv()), factories{tasks: func(context.Context) (gcp.TasksAPI, func() error, error) { return client, nil, nil }, validate: idtoken.Validate})
	if err != nil {
		t.Fatal(err)
	}
	in := projection.Request{Root: sampleSubmission().Events[0].Root}
	if err = r.Replay.Enqueue(context.Background(), in); err == nil || len(client.requests) != 0 {
		t.Fatal("missing run reached tasks", err)
	}
	for _, run := range []projection.RunID{"run-1", "run-1", "run-2"} {
		in.RunID = run
		if err = r.Replay.Enqueue(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	if client.requests[0].Task.Name != client.requests[1].Task.Name || client.requests[0].Task.Name == client.requests[2].Task.Name {
		t.Fatal("Tasks deduplicated new logical replay")
	}
	for i, request := range client.requests {
		envelope, err := delivery.Decode(request.Task.GetHttpRequest().Body)
		if err != nil || envelope.Replay.RunID != []projection.RunID{"run-1", "run-1", "run-2"}[i] {
			t.Fatal(envelope, err)
		}
	}
}
