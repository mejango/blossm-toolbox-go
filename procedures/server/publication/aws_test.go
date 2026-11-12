package runtimepub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	canonicaljson "github.com/mejango/blossm-toolbox-go/lib/canonical_json"
	eventlib "github.com/mejango/blossm-toolbox-go/lib/event"
	"github.com/mejango/blossm-toolbox-go/procedures/event_stores/generic"
)

const awsTestTopic = "arn:aws:sns:us-east-1:123456789012:events"

type awsTestTransport func(*http.Request) (*http.Response, error)

func (f awsTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Use real SDK serialization/signing, but reject every destination except this fixture.
func awsTestClients(t *testing.T, handler http.HandlerFunc) (*sns.Client, *sqs.Client, string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("SDK signature missing")
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	transport := server.Client().Transport
	client := &http.Client{Transport: awsTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme+"://"+r.URL.Host != server.URL {
			return nil, fmt.Errorf("nonlocal request rejected: %s", r.URL.Host)
		}
		return transport.RoundTrip(r)
	}), CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect rejected") }}
	cfg := aws.Config{Region: "us-east-1", BaseEndpoint: aws.String(server.URL), HTTPClient: client, RetryMaxAttempts: 1,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "synthetic-access", SecretAccessKey: "synthetic-secret"}, nil
		})}
	return sns.NewFromConfig(cfg), sqs.NewFromConfig(cfg), server.URL + "/123456789012/events"
}
func awsTestOptions() Options {
	return Options{PullTimeout: 100 * time.Millisecond, HandleTimeout: 30 * time.Millisecond, AckTimeout: 30 * time.Millisecond, RetryDelay: 10 * time.Millisecond}
}
func awsTestJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	_ = json.NewEncoder(w).Encode(body)
}
func awsTestRuntime(t *testing.T, p *sns.Client, s *sqs.Client, queue string, opts Options) *Runtime {
	t.Helper()
	r, err := NewAWS(p, s, awsTestTopic, queue, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}
func awsTestWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func TestAWSPublishWireAndValidation(t *testing.T) {
	for _, mode := range []string{"valid", "wrong ID", "tampered event", "invalid JSON", "provider error", "missing acknowledgment"} {
		t.Run(mode, func(t *testing.T) {
			notice := validNotice(t)
			switch mode {
			case "wrong ID":
				notice.ID = "wrong"
			case "tampered event":
				notice.Event.Input.Action += "tampered"
			case "invalid JSON":
				notice.Event.Input.Payload = json.RawMessage(`{`)
			}
			var calls atomic.Int32
			p, s, queue := awsTestClients(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				want, _ := canonicaljson.Encode(notice)
				for key, value := range map[string]string{"Action": "Publish", "TopicArn": awsTestTopic, "Message": string(want), "MessageAttributes.entry.1.Name": "eventId", "MessageAttributes.entry.1.Value.DataType": "String", "MessageAttributes.entry.1.Value.StringValue": notice.ID} {
					if got := r.Form.Get(key); got != value {
						t.Errorf("%s = %q, want %q", key, got, value)
					}
				}
				w.Header().Set("Content-Type", "text/xml")
				if mode == "provider error" {
					w.WriteHeader(503)
					_, _ = io.WriteString(w, `<ErrorResponse><Error><Code>ServiceUnavailable</Code><Message>retry</Message></Error></ErrorResponse>`)
					return
				}
				id := "published"
				if mode == "missing acknowledgment" {
					id = ""
				}
				_, _ = fmt.Fprintf(w, `<PublishResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>%s</MessageId></PublishResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></PublishResponse>`, id)
			})
			runtime := awsTestRuntime(t, p, s, queue, awsTestOptions())
			err := runtime.Publish(context.Background(), notice)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("Publish error = %v", err)
			}
			wantCalls := int32(1)
			if mode == "wrong ID" || mode == "tampered event" || mode == "invalid JSON" {
				wantCalls = 0
			}
			if calls.Load() != wantCalls {
				t.Fatalf("requests = %d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestAWSConsumeWireValidationAndRetention(t *testing.T) {
	for _, mode := range []string{"raw", "raw without attribute", "envelope", "envelope without attribute", "malformed", "tampered", "wrong ID", "wrong attribute", "empty attribute", "missing receipt", "wrong envelope attribute", "wrong topic", "wrong envelope type", "handler error", "expired handler", "canceled handler", "delete error", "delete timeout", "receive error"} {
		t.Run(mode, func(t *testing.T) {
			notice := validNotice(t)
			encoded, err := canonicaljson.Encode(notice)
			if err != nil {
				t.Fatal(err)
			}
			body := string(encoded)
			attribute := notice.ID
			switch mode {
			case "malformed":
				body = `{"id":`
			case "tampered":
				n := notice
				n.Event.Input.Action += "tampered"
				b, _ := canonicaljson.Encode(n)
				body = string(b)
			case "wrong ID":
				n := notice
				n.ID = "wrong"
				b, _ := canonicaljson.Encode(n)
				body = string(b)
			case "wrong attribute":
				attribute = "wrong"
			case "empty attribute":
				attribute = ""
			}
			if strings.Contains(mode, "envelope") || mode == "wrong topic" {
				topic, kind, id := awsTestTopic, "Notification", notice.ID
				if mode == "wrong topic" {
					topic += "other"
				}
				if mode == "wrong envelope type" {
					kind = "SubscriptionConfirmation"
				}
				if mode == "wrong envelope attribute" {
					id = "wrong"
				}
				envelope := map[string]any{"Type": kind, "TopicArn": topic, "Message": body, "MessageId": "sns-message"}
				if mode != "envelope without attribute" {
					envelope["MessageAttributes"] = map[string]any{"eventId": map[string]string{"Type": "String", "Value": id}}
				}
				b, _ := json.Marshal(envelope)
				body = string(b)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var receives, deletes, handled atomic.Int32
			var failureAt atomic.Int64
			var logs bytes.Buffer
			p, s, queue := awsTestClients(t, func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					QueueURL   string   `json:"QueueUrl"`
					Receipt    string   `json:"ReceiptHandle"`
					Max        int      `json:"MaxNumberOfMessages"`
					Wait       int      `json:"WaitTimeSeconds"`
					Visibility int      `json:"VisibilityTimeout"`
					Attributes []string `json:"MessageAttributeNames"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				if input.QueueURL != "http://"+r.Host+"/123456789012/events" {
					t.Errorf("wrong queue %q", input.QueueURL)
				}
				switch strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSQS.") {
				case "ReceiveMessage":
					if input.Max != 1 || input.Wait < 0 || input.Wait > 20 || input.Visibility != 60 {
						t.Errorf("unsafe receive settings: %+v", input)
					}
					found := false
					for _, a := range input.Attributes {
						if a == "eventId" || a == "All" {
							found = true
						}
					}
					if !found {
						t.Error("eventId attribute not requested")
					}
					if receives.Add(1) > 1 {
						if failureAt.Load() != 0 && time.Since(time.Unix(0, failureAt.Load())) < 8*time.Millisecond {
							t.Error("provider failure did not back off")
						}
						cancel()
						awsTestJSON(w, map[string]any{})
						return
					}
					if mode == "receive error" {
						failureAt.Store(time.Now().UnixNano())
						w.WriteHeader(503)
						_, _ = io.WriteString(w, `{"__type":"ServiceUnavailable","message":"retry"}`)
						return
					}
					message := map[string]any{"MessageId": "sqs-message-not-event-id", "Body": body, "ReceiptHandle": "original+/receipt=="}
					if mode == "missing receipt" {
						delete(message, "ReceiptHandle")
					}
					if mode != "raw without attribute" {
						message["MessageAttributes"] = map[string]any{"eventId": map[string]string{"DataType": "String", "StringValue": attribute}}
					}
					awsTestJSON(w, map[string]any{"Messages": []any{message}})
				case "DeleteMessage":
					deletes.Add(1)
					if input.Receipt != "original+/receipt==" {
						t.Errorf("receipt = %q", input.Receipt)
					}
					if mode == "delete error" {
						failureAt.Store(time.Now().UnixNano())
						w.WriteHeader(400)
						_, _ = io.WriteString(w, `{"__type":"ReceiptHandleIsInvalid","message":"expired"}`)
						return
					}
					if mode == "delete timeout" {
						<-r.Context().Done()
						return
					}
					awsTestJSON(w, map[string]any{})
				default:
					t.Errorf("unexpected SQS operation %q", r.Header.Get("X-Amz-Target"))
					w.WriteHeader(400)
				}
			})
			runtime := awsTestRuntime(t, p, s, queue, awsTestOptions())
			runtime.Consume(ctx, publishFunc(func(hctx context.Context, got eventlib.Publication) error {
				handled.Add(1)
				if got.ID != notice.ID || eventstore.Verify(got.Event) != nil {
					t.Error("handler received unverified notice")
				}
				if _, ok := hctx.Deadline(); !ok {
					t.Error("handler context lacks deadline")
				}
				switch mode {
				case "handler error":
					return errors.New("handler failed")
				case "expired handler":
					<-hctx.Done()
					return nil // A late nil is not success.
				case "canceled handler":
					cancel()
					<-hctx.Done()
					return nil
				}
				return nil
			}), slog.New(slog.NewJSONHandler(&logs, nil)))
			valid := mode == "raw" || mode == "raw without attribute" || mode == "envelope" || mode == "envelope without attribute" || strings.HasPrefix(mode, "handler") || mode == "expired handler" || mode == "canceled handler" || strings.HasPrefix(mode, "delete")
			wantHandled := int32(0)
			if valid {
				wantHandled = 1
			}
			wantDelete := int32(0)
			if valid && mode != "handler error" && mode != "expired handler" && mode != "canceled handler" {
				wantDelete = 1
			}
			if handled.Load() != wantHandled || deletes.Load() != wantDelete {
				t.Fatalf("handler/delete calls = %d/%d, want %d/%d", handled.Load(), deletes.Load(), wantHandled, wantDelete)
			}
			if (mode == "delete error" || mode == "delete timeout" || mode == "receive error") && !strings.Contains(logs.String(), `"level":"ERROR"`) {
				t.Fatalf("provider failure not logged: %s", logs.String())
			}
		})
	}
}

func TestAWSCloseCancelsAndWaitsForHandler(t *testing.T) {
	notice := validNotice(t)
	body, _ := canonicaljson.Encode(notice)
	entered, canceled, release, consumed, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var deletes atomic.Int32
	p, s, queue := awsTestClients(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.Header.Get("X-Amz-Target"), "DeleteMessage") {
			deletes.Add(1)
			awsTestJSON(w, map[string]any{})
			return
		}
		awsTestJSON(w, map[string]any{"Messages": []any{map[string]any{"Body": string(body), "ReceiptHandle": "original"}}})
	})
	runtime := awsTestRuntime(t, p, s, queue, Options{HandleTimeout: time.Second})
	// Always release the deliberately uncooperative callback before cleanup closes runtime.
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		defer close(consumed)
		runtime.Consume(context.Background(), publishFunc(func(ctx context.Context, _ eventlib.Publication) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-release
			return nil
		}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	awsTestWait(t, entered, "handler")
	go func() { _ = runtime.Close(); close(closed) }()
	awsTestWait(t, canceled, "handler cancellation")
	select {
	case <-closed:
		t.Fatal("Close returned before active handler finished")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	awsTestWait(t, closed, "Close")
	awsTestWait(t, consumed, "Consume")
	if deletes.Load() != 0 {
		t.Fatal("canceled handler acknowledged")
	}
	if err := runtime.Publish(context.Background(), notice); err == nil {
		t.Fatal("closed runtime accepted publication")
	}
}

func TestAWSRequestsCanceledByCallerAndClose(t *testing.T) {
	for _, operation := range []string{"Publish", "ReceiveMessage"} {
		for _, stop := range []string{"caller", "Close"} {
			t.Run(operation+"/"+stop, func(t *testing.T) {
				entered, finished := make(chan struct{}), make(chan struct{})
				p, s, queue := awsTestClients(t, func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					close(entered)
					<-r.Context().Done()
				})
				runtime := awsTestRuntime(t, p, s, queue, Options{PullTimeout: time.Second})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				notice := validNotice(t)
				go func() {
					defer close(finished)
					if operation == "Publish" {
						if err := runtime.Publish(ctx, notice); err == nil {
							t.Error("canceled publish succeeded")
						}
						return
					}
					runtime.Consume(ctx, publishFunc(func(context.Context, eventlib.Publication) error {
						t.Error("handler invoked without delivery")
						return nil
					}), slog.New(slog.NewTextHandler(io.Discard, nil)))
				}()
				awsTestWait(t, entered, "HTTP request")
				if stop == "caller" {
					cancel()
				} else {
					done := make(chan struct{})
					go func() { _ = runtime.Close(); close(done) }()
					awsTestWait(t, done, "Close with in-flight request")
				}
				awsTestWait(t, finished, "request cancellation")
			})
		}
	}
}

func TestAWSCloseWaitsForCanceledPublishRequest(t *testing.T) {
	entered, canceled, release, finished, closed := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	p, s, queue := awsTestClients(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	})
	client := p.Options().HTTPClient.(*http.Client)
	guarded := client.Transport
	// Keep the real SDK request in flight after HTTP cancellation until released.
	client.Transport = awsTestTransport(func(r *http.Request) (*http.Response, error) {
		response, err := guarded.RoundTrip(r)
		if r.Context().Err() == nil {
			t.Error("request was not canceled")
		}
		close(canceled)
		<-release
		return response, err
	})
	runtime := awsTestRuntime(t, p, s, queue, awsTestOptions())
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	notice := validNotice(t)
	go func() {
		defer close(finished)
		if err := runtime.Publish(context.Background(), notice); err == nil {
			t.Error("canceled publication succeeded")
		}
	}()
	awsTestWait(t, entered, "publish request")
	go func() { _ = runtime.Close(); close(closed) }()
	awsTestWait(t, canceled, "transport cancellation")
	select {
	case <-closed:
		t.Fatal("Close returned before active request finished")
	case <-time.After(20 * time.Millisecond):
	}
	unblock()
	awsTestWait(t, closed, "Close")
	awsTestWait(t, finished, "Publish")
}

func TestAWSPublishSizePreflight(t *testing.T) {
	var calls atomic.Int32
	p, s, queue := awsTestClients(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		data := []byte(r.Form.Get("Message"))
		var notice eventlib.Publication
		if err := canonicaljson.Decode(data, &notice); err != nil || notice.Attempts != 123456 || len(data) != eventstore.MaxPublicationBytes {
			t.Error("wire size or Attempts changed", err)
		}
		if r.Form.Get("Action") != "Publish" {
			t.Error("unexpected SDK method")
		}
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, `<PublishResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><PublishResult><MessageId>accepted</MessageId></PublishResult><ResponseMetadata><RequestId>r</RequestId></ResponseMetadata></PublishResponse>`)
	})
	runtime := awsTestRuntime(t, p, s, queue, Options{})
	checkPublishSize(t, runtime, &calls)
}
