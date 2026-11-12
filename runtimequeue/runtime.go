// Package runtimequeue composes configured SDK clients with delivery adapters.
// Open constructs clients only; workers start exclusively through Run.
package runtimequeue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	cloudaws "blossm.local/backend/cloud/aws"
	"blossm.local/backend/cloud/gcp"
	"blossm.local/backend/command"
	"blossm.local/backend/delivery"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/projection"
	"blossm.local/backend/transport"
	tasks "cloud.google.com/go/cloudtasks/apiv2"
	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"google.golang.org/api/idtoken"
)

var ErrDeliveryUnauthorized = errors.New("unauthorized delivery identity")

type Runtime struct {
	Commands         command.Queue
	Replay           projection.Producer
	Publications     eventstore.Publisher
	DeliveryVerifier transport.TokenVerifier

	sqs         *delivery.SQSTarget
	timeout     time.Duration
	closeClient func() error
	closeOnce   sync.Once
	closeErr    error
	wait        func(context.Context, time.Duration) error
}

type factories struct {
	tasks    func(context.Context) (gcp.TasksAPI, func() error, error)
	sqs      func(context.Context) (cloudaws.SQSAPI, error)
	validate func(context.Context, string, string) (*idtoken.Payload, error)
}

// Open reads provider-specific configuration before discovering credentials.
// Local mode has no queues; the application owns its synchronous execution.
func Open(ctx context.Context, provider string) (*Runtime, error) {
	return open(ctx, provider, os.Getenv, factories{
		tasks: func(ctx context.Context) (gcp.TasksAPI, func() error, error) {
			c, err := tasks.NewClient(ctx)
			if err != nil {
				return nil, nil, err
			}
			return c, c.Close, nil
		},
		sqs: func(ctx context.Context) (cloudaws.SQSAPI, error) {
			c, err := config.LoadDefaultConfig(ctx)
			if err != nil {
				return nil, err
			}
			if c.Region == "" {
				return nil, errors.New("AWS region required")
			}
			return sqs.NewFromConfig(c), nil
		},
		validate: idtoken.Validate,
	})
}

func open(ctx context.Context, provider string, getenv func(string) string, f factories) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r := &Runtime{DeliveryVerifier: denyHTTP, wait: waitContext, timeout: time.Minute}
	if provider == "local" {
		return r, nil
	}
	if provider != "gcp" && provider != "aws" {
		return nil, fmt.Errorf("unsupported delivery provider %q", provider)
	}
	if provider == "gcp" {
		if getenv("DELIVERY_TIMEOUT") != "" {
			return nil, errors.New("DELIVERY_TIMEOUT applies only to SQS workers")
		}
		queue, target, account, audience := getenv("DELIVERY_TASKS_QUEUE"), getenv("DELIVERY_URL"), getenv("DELIVERY_SERVICE_ACCOUNT"), getenv("DELIVERY_AUDIENCE")
		parts := strings.Split(queue, "/")
		if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "queues" || parts[1] == "" || parts[3] == "" || parts[5] == "" {
			return nil, errors.New("DELIVERY_TASKS_QUEUE must name projects/.../locations/.../queues/...")
		}
		if !httpsURL(target) {
			return nil, errors.New("DELIVERY_URL must be an absolute HTTPS URL")
		}
		if strings.TrimSpace(audience) == "" || strings.TrimSpace(account) != account || !strings.HasSuffix(account, ".gserviceaccount.com") || strings.Count(account, "@") != 1 || strings.HasPrefix(account, "@") {
			return nil, errors.New("DELIVERY_AUDIENCE and DELIVERY_SERVICE_ACCOUNT required")
		}
		if f.tasks == nil || f.validate == nil {
			return nil, errors.New("Tasks client and ID token validator required")
		}
		client, closeClient, err := f.tasks(ctx)
		if err != nil {
			return nil, err
		}
		r.closeClient = closeClient
		if client == nil {
			_ = r.Close()
			return nil, errors.New("Tasks client missing")
		}
		if err = ctx.Err(); err != nil {
			_ = r.Close()
			return nil, err
		}
		t := delivery.TasksTarget{Tasks: gcp.Tasks{Client: client}, Queue: queue, Dispatch: gcp.HTTPDispatch{URL: target, ServiceAccount: account, Audience: audience}}
		r.Commands = delivery.TasksCommands{Target: t}
		r.Publications = delivery.TasksPublications{Target: t}
		r.Replay = delivery.TasksReplay{Target: t}
		r.DeliveryVerifier = googleVerifier(audience, account, f.validate)
		return r, nil
	}
	queueURL, group := getenv("DELIVERY_QUEUE_URL"), getenv("DELIVERY_GROUP_ID")
	if !httpsURL(queueURL) {
		return nil, errors.New("DELIVERY_QUEUE_URL must be an absolute HTTPS URL")
	}
	u, _ := url.Parse(queueURL)
	if u.RawQuery != "" || u.Path == "" || u.Path == "/" {
		return nil, errors.New("DELIVERY_QUEUE_URL must name a queue without query parameters")
	}
	fifo := strings.HasSuffix(queueURL, ".fifo")
	if (fifo && (strings.TrimSpace(group) == "" || len(group) > 128)) || (!fifo && group != "") {
		return nil, errors.New("DELIVERY_GROUP_ID required only for FIFO queues (max128 bytes)")
	}
	if raw := getenv("DELIVERY_TIMEOUT"); raw != "" {
		timeout, err := time.ParseDuration(raw)
		if err != nil || timeout < time.Second || timeout > 11*time.Hour {
			return nil, errors.New("DELIVERY_TIMEOUT must be1s..11h")
		}
		r.timeout = timeout
	}
	if f.sqs == nil {
		return nil, errors.New("SQS client factory required")
	}
	client, err := f.sqs(ctx)
	if err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("SQS client missing")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	target := delivery.SQSTarget{Queue: cloudaws.SQS{Client: client}, QueueURL: queueURL, GroupID: group}
	r.sqs = &target
	r.Commands = delivery.SQSCommands{Target: target}
	r.Publications = delivery.SQSPublications{Target: target}
	r.Replay = delivery.SQSReplay{Target: target}
	return r, nil
}
func httpsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func denyHTTP(context.Context, string) (transport.Identity, error) {
	return transport.Identity{}, ErrDeliveryUnauthorized
}

// Google validates cryptographic signature and token lifetime. These additional
// checks restrict accepted tokens to this exact queue producer and route audience.
func googleVerifier(audience, account string, validate func(context.Context, string, string) (*idtoken.Payload, error)) transport.TokenVerifier {
	return func(ctx context.Context, token string) (transport.Identity, error) {
		if err := ctx.Err(); err != nil {
			return transport.Identity{}, err
		}
		if token == "" || len(token) > 16384 {
			return transport.Identity{}, ErrDeliveryUnauthorized
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		p, err := validate(ctx, token, audience)
		if err != nil || p == nil {
			return transport.Identity{}, ErrDeliveryUnauthorized
		}
		if err = ctx.Err(); err != nil {
			return transport.Identity{}, err
		}
		email, emailOK := p.Claims["email"].(string)
		verified, verifiedOK := p.Claims["email_verified"].(bool)
		if (p.Issuer != "https://accounts.google.com" && p.Issuer != "accounts.google.com") || p.Audience != audience || p.Subject == "" || p.Expires <= time.Now().Unix() || !emailOK || email != account || !verifiedOK || !verified {
			return transport.Identity{}, ErrDeliveryUnauthorized
		}
		claims, err := json.Marshal(p.Claims)
		if err != nil {
			return transport.Identity{}, ErrDeliveryUnauthorized
		}
		return transport.Identity{Claims: claims}, nil
	}
}

// Run consumes one SQS message at a time. The deadline includes long polling,
// processing and acknowledgment; visibility exceeds it by30s. Failure leaves the
// receipt unacknowledged. GCP HTTP consumption and local work have no poller.
func (r *Runtime) Run(ctx context.Context, consumer *delivery.Consumer, logger *slog.Logger) error {
	if r == nil {
		return errors.New("delivery runtime required")
	}
	if r.sqs == nil {
		return nil
	}
	if consumer == nil || consumer.Admit == nil {
		return errors.New("delivery consumer with admission required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	worker := delivery.SQSWorker{Queue: r.sqs.Queue, Consumer: consumer}
	visibility := int32((r.timeout+time.Second-1)/time.Second) + 30
	waitSeconds := min(int32(20), int32(r.timeout/(3*time.Second)))
	input := &sqs.ReceiveMessageInput{QueueUrl: sdk.String(r.sqs.QueueURL), MaxNumberOfMessages: 1, WaitTimeSeconds: waitSeconds, VisibilityTimeout: visibility}
	backoff := time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempt, cancel := context.WithTimeout(ctx, r.timeout)
		_, err := worker.Poll(attempt, input)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		delay := time.Duration(0)
		if err != nil {
			logger.WarnContext(ctx, "delivery poll failed; receipt remains unacknowledged", "category", "delivery_failure", "canceled", errors.Is(err, context.Canceled), "deadline", errors.Is(err, context.DeadlineExceeded))
			delay = backoff
			backoff = min(backoff*2, 30*time.Second)
		} else {
			backoff = time.Second
			delay = 100 * time.Millisecond
		} // Bound empty-response polling even if the provider returns immediately.
		if err = r.wait(ctx, delay); err != nil {
			return err
		}
	}
}
func waitContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Close is idempotent. Cancel Run and wait for it before closing owned clients.
func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.closeClient != nil {
			r.closeErr = r.closeClient()
		}
	})
	return r.closeErr
}
