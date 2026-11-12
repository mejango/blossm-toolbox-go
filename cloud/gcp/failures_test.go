package gcp

import (
	tasks "cloud.google.com/go/cloudtasks/apiv2"
	kms "cloud.google.com/go/kms/apiv1"
	logging "cloud.google.com/go/logging/apiv2"
	pubsub "cloud.google.com/go/pubsub/v2/apiv1"
	pp "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	scheduler "cloud.google.com/go/scheduler/apiv1"
	"cloud.google.com/go/storage"
	"context"
	"errors"
	"github.com/googleapis/gax-go/v2"
	mr "google.golang.org/genproto/googleapis/api/monitoredres"
	lt "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"reflect"
	"testing"
	"time"
)

func TestUnconfiguredClientsFailWithoutPanics(t *testing.T) {
	// Every exported operation gets a context and zero arguments. This guards
	// both plain and typed-nil client interfaces without accessing credentials.
	for _, adapter := range []any{Tasks{}, Scheduler{}, PubSub{}, KMS{}, Storage{}, Logging{}, Secrets{}, Tasks{(*tasks.Client)(nil)}, Scheduler{(*scheduler.CloudSchedulerClient)(nil)}, PubSub{(*pubsub.TopicAdminClient)(nil), (*pubsub.SubscriptionAdminClient)(nil)}, KMS{(*kms.KeyManagementClient)(nil)}, Storage{(*storage.Client)(nil)}, Logging{Client: (*logging.Client)(nil)}} {
		value := reflect.ValueOf(adapter)
		for i := 0; i < value.NumMethod(); i++ {
			method := value.Method(i)
			name := value.Type().Name() + "/" + value.Type().Method(i).Name
			t.Run(name, func(t *testing.T) {
				args := make([]reflect.Value, method.Type().NumIn())
				args[0] = reflect.ValueOf(context.Background())
				for j := 1; j < len(args); j++ {
					args[j] = reflect.Zero(method.Type().In(j))
				}
				var out []reflect.Value
				if method.Type().IsVariadic() {
					out = method.CallSlice(args)
				} else {
					out = method.Call(args)
				}
				if out[len(out)-1].IsNil() {
					t.Fatal("unconfigured adapter succeeded")
				}
			})
		}
	}
}
func TestProviderErrorsAndCancellation(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "permission", true: "cancel"}[cancel], func(t *testing.T) {
			opts := wire(t, func(ctx context.Context, _ string, _ any) (any, error) {
				if cancel {
					<-ctx.Done()
					return nil, status.FromContextError(ctx.Err()).Err()
				}
				return nil, status.Error(codes.PermissionDenied, "denied")
			})
			ctx := context.Background()
			tc, err := tasks.NewClient(ctx, opts...)
			must(t, err)
			defer tc.Close()
			sc, err := scheduler.NewCloudSchedulerClient(ctx, opts...)
			must(t, err)
			defer sc.Close()
			pc, err := pubsub.NewTopicAdminClient(ctx, opts...)
			must(t, err)
			defer pc.Close()
			sub, err := pubsub.NewSubscriptionAdminClient(ctx, opts...)
			must(t, err)
			defer sub.Close()
			kc, err := kms.NewKeyManagementClient(ctx, opts...)
			must(t, err)
			defer kc.Close()
			lc, err := logging.NewClient(ctx, opts...)
			must(t, err)
			defer lc.Close()
			h := HTTPDispatch{URL: "https://service.example", ServiceAccount: "executor@p.iam.gserviceaccount.com"}
			p := PubSub{pc, sub}
			k := KMS{kc}
			operations := map[string]func(context.Context) error{
				"task": func(c context.Context) error {
					_, e := (Tasks{tc}).Enqueue(c, location+"/queues/q", "", h, time.Time{})
					return e
				},
				"schedule": func(c context.Context) error {
					_, e := (Scheduler{sc}).Create(c, location, Schedule{Name: location + "/jobs/j", Cron: "* * * * *", TimeZone: "UTC", Target: h})
					return e
				},
				"publish": func(c context.Context) error {
					_, e := p.PublishBytes(c, "projects/p/topics/t", []byte("data"), nil)
					return e
				},
				"pull":    func(c context.Context) error { _, e := p.Pull(c, "projects/p/subscriptions/s", 1); return e },
				"ack":     func(c context.Context) error { return p.Ack(c, "projects/p/subscriptions/s", "ack") },
				"nack":    func(c context.Context) error { return p.Nack(c, "projects/p/subscriptions/s", "ack") },
				"encrypt": func(c context.Context) error { _, e := k.Encrypt(c, key, []byte("data")); return e },
				"decrypt": func(c context.Context) error { _, e := k.Decrypt(c, key, []byte("data")); return e },
				"sign":    func(c context.Context) error { _, e := k.Sign(c, version, []byte("data")); return e },
				"public":  func(c context.Context) error { _, e := k.PublicKey(c, version); return e },
				"log": func(c context.Context) error {
					return (Logging{Client: lc, LogName: "projects/p/logs/app", Resource: &mr.MonitoredResource{Type: "global"}}).Write(c, lt.LogSeverity_ERROR, "", time.Time{}, map[string]any{"message": "failed"})
				},
			}
			for name, fn := range operations {
				t.Run(name, func(t *testing.T) {
					// Permission propagation has no sub-second latency contract.
					// Keep the short deadline only in the cancellation fixture.
					budget := 5 * time.Second
					if cancel {
						budget = 100 * time.Millisecond
					}
					c, stop := context.WithTimeout(ctx, budget)
					defer stop()
					e := fn(c)
					want := codes.PermissionDenied
					if cancel {
						want = codes.DeadlineExceeded
					}
					if status.Code(e) != want && !(cancel && errors.Is(e, context.DeadlineExceeded)) {
						t.Fatalf("want %v, got %v", want, e)
					}
				})
			}
		})
	}
}

type nilPull struct{ SubscriberAPI }

func (nilPull) Pull(context.Context, *pp.PullRequest, ...gax.CallOption) (*pp.PullResponse, error) {
	return nil, nil
}

type nilPublish struct{ PublisherAPI }

func (nilPublish) Publish(context.Context, *pp.PublishRequest, ...gax.CallOption) (*pp.PublishResponse, error) {
	return nil, nil
}
func TestMalformedSDKResponsesAndDispatch(t *testing.T) {
	ctx := context.Background()
	if _, err := (PubSub{Subscriber: nilPull{}}).Pull(ctx, "projects/p/subscriptions/s", 1); err == nil {
		t.Fatal("nil pull accepted")
	}
	if _, err := (PubSub{Publisher: nilPublish{}}).PublishBytes(ctx, "projects/p/topics/t", nil, nil); err == nil {
		t.Fatal("nil publish accepted")
	}
	h := HTTPDispatch{URL: "https://service.example", ServiceAccount: "executor@p.iam.gserviceaccount.com", Data: make(chan int)}
	if _, _, _, err := h.prepare(); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	h.Data = nil
	h.BearerToken = "bearer"
	if _, _, _, err := h.prepare(); err == nil {
		t.Fatal("ambiguous auth")
	}
	h.ServiceAccount = ""
	h.Audience = "ignored"
	if _, _, _, err := h.prepare(); err == nil {
		t.Fatal("ignored audience")
	}
	h.Audience = ""
	h.URL = "http://insecure.example"
	if _, _, _, err := h.prepare(); err == nil {
		t.Fatal("insecure auth")
	}
	c, stop := context.WithCancel(ctx)
	stop()
	if !errors.Is(ready(c, struct{}{}), context.Canceled) {
		t.Fatal("cancellation lost")
	}
}
