// Package gcp implements the retained Google Cloud boundaries. Clients and their
// lifetimes are caller-owned; importing this package never discovers credentials.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	tasks "cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type TasksAPI interface {
	CreateTask(context.Context, *tasks.CreateTaskRequest, ...gax.CallOption) (*tasks.Task, error)
	CreateQueue(context.Context, *tasks.CreateQueueRequest, ...gax.CallOption) (*tasks.Queue, error)
	GetQueue(context.Context, *tasks.GetQueueRequest, ...gax.CallOption) (*tasks.Queue, error)
	UpdateQueue(context.Context, *tasks.UpdateQueueRequest, ...gax.CallOption) (*tasks.Queue, error)
}
type Tasks struct{ Client TasksAPI }

// HTTPDispatch contains JSON data and exactly one authentication mode. OIDC's
// default audience is the origin, excluding the route and query parameters.
type HTTPDispatch struct {
	URL, Method, ServiceAccount, Audience, BearerToken string
	Data                                               any
}

func (h HTTPDispatch) prepare() ([]byte, string, string, error) {
	u, err := url.Parse(h.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, "", "", fmt.Errorf("gcp: expected an absolute HTTPS target")
	}
	method := strings.ToUpper(h.Method)
	if method == "" {
		method = "POST"
	}
	switch method {
	case "POST", "PUT", "PATCH", "DELETE", "GET", "HEAD", "OPTIONS":
	default:
		return nil, "", "", fmt.Errorf("gcp: unsupported HTTP method %q", method)
	}
	if (h.ServiceAccount == "") == (h.BearerToken == "") {
		return nil, "", "", fmt.Errorf("gcp: select exactly one of OIDC service account or bearer token")
	}
	if h.BearerToken != "" && h.Audience != "" {
		return nil, "", "", fmt.Errorf("gcp: audience applies only to OIDC")
	}
	audience := h.Audience
	if audience == "" {
		audience = u.Scheme + "://" + u.Host
	}
	var body []byte
	if h.Data != nil {
		body, err = json.Marshal(h.Data)
		if err != nil {
			return nil, "", "", err
		}
	}
	if len(body) > 0 && method != "POST" && method != "PUT" && method != "PATCH" {
		return nil, "", "", fmt.Errorf("gcp: %s target cannot carry JSON body", method)
	}
	return body, method, audience, nil
}

// Enqueue returns only after Cloud Tasks acknowledges creation. A stable name
// enables caller-controlled deduplication; AlreadyExists is returned unchanged.
// name must be empty or a full projects/.../queues/.../tasks/... resource name.
func (t Tasks) Enqueue(ctx context.Context, queue, name string, h HTTPDispatch, at time.Time) (*tasks.Task, error) {
	if err := ready(ctx, t.Client); err != nil {
		return nil, err
	}

	body, method, audience, err := h.prepare()
	if err != nil {
		return nil, err
	}

	req := &tasks.HttpRequest{Url: h.URL, HttpMethod: tasks.HttpMethod(tasks.HttpMethod_value[method]), Body: body, Headers: map[string]string{"Content-Type": "application/json"}}
	if h.BearerToken != "" {
		req.Headers["Authorization"] = "Bearer " + h.BearerToken
	} else {
		req.AuthorizationHeader = &tasks.HttpRequest_OidcToken{OidcToken: &tasks.OidcToken{ServiceAccountEmail: h.ServiceAccount, Audience: audience}}
	}
	task := &tasks.Task{Name: name, MessageType: &tasks.Task_HttpRequest{HttpRequest: req}}
	if !at.IsZero() {
		task.ScheduleTime = timestamppb.New(at)
		if err := task.ScheduleTime.CheckValid(); err != nil {
			return nil, err
		}
	}
	return t.Client.CreateTask(ctx, &tasks.CreateTaskRequest{Parent: queue, Task: task})
}
func queueRates(rate float64, concurrent int32) (*tasks.RateLimits, error) {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) || concurrent <= 0 {
		return nil, fmt.Errorf("gcp: queue rates must be positive and finite")
	}
	return &tasks.RateLimits{MaxDispatchesPerSecond: rate, MaxConcurrentDispatches: concurrent}, nil
}
func (t Tasks) CreateQueue(ctx context.Context, parent, name string, rate float64, concurrent int32) (*tasks.Queue, error) {
	if err := ready(ctx, t.Client); err != nil {
		return nil, err
	}

	limits, err := queueRates(rate, concurrent)
	if err != nil {
		return nil, err
	}
	return t.Client.CreateQueue(ctx, &tasks.CreateQueueRequest{Parent: parent, Queue: &tasks.Queue{Name: name, RateLimits: limits, StackdriverLoggingConfig: &tasks.StackdriverLoggingConfig{SamplingRatio: 0.1}}})
}
func (t Tasks) QueueExists(ctx context.Context, name string) (bool, error) {
	if err := ready(ctx, t.Client); err != nil {
		return false, err
	}
	_, err := t.Client.GetQueue(ctx, &tasks.GetQueueRequest{Name: name})
	return exists(err)
}
func (t Tasks) UpdateRates(ctx context.Context, name string, rate float64, concurrent int32) (*tasks.Queue, error) {
	if err := ready(ctx, t.Client); err != nil {
		return nil, err
	}

	limits, err := queueRates(rate, concurrent)
	if err != nil {
		return nil, err
	}
	return t.Client.UpdateQueue(ctx, &tasks.UpdateQueueRequest{Queue: &tasks.Queue{Name: name, RateLimits: limits}, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"rate_limits.max_dispatches_per_second", "rate_limits.max_concurrent_dispatches"}}})
}
