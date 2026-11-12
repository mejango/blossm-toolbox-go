package gcp

import (
	pb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"context"
	"encoding/json"
	"fmt"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
)

type PublisherAPI interface {
	Publish(context.Context, *pb.PublishRequest, ...gax.CallOption) (*pb.PublishResponse, error)
	CreateTopic(context.Context, *pb.Topic, ...gax.CallOption) (*pb.Topic, error)
	GetTopic(context.Context, *pb.GetTopicRequest, ...gax.CallOption) (*pb.Topic, error)
	DeleteTopic(context.Context, *pb.DeleteTopicRequest, ...gax.CallOption) error
}
type SubscriberAPI interface {
	CreateSubscription(context.Context, *pb.Subscription, ...gax.CallOption) (*pb.Subscription, error)
	GetSubscription(context.Context, *pb.GetSubscriptionRequest, ...gax.CallOption) (*pb.Subscription, error)
	DeleteSubscription(context.Context, *pb.DeleteSubscriptionRequest, ...gax.CallOption) error
	Pull(context.Context, *pb.PullRequest, ...gax.CallOption) (*pb.PullResponse, error)
	Acknowledge(context.Context, *pb.AcknowledgeRequest, ...gax.CallOption) error
	ModifyAckDeadline(context.Context, *pb.ModifyAckDeadlineRequest, ...gax.CallOption) error
}
type PubSub struct {
	Publisher  PublisherAPI
	Subscriber SubscriberAPI
}

func exists(err error) (bool, error) {
	if status.Code(err) == codes.NotFound {
		return false, nil
	}
	return err == nil, err
}
func ignoreNotFound(err error) error {
	if status.Code(err) == codes.NotFound {
		return nil
	}
	return err
}
func (p PubSub) Publish(ctx context.Context, topic string, data any, attributes map[string]string) (string, error) {
	if err := ready(ctx, p.Publisher); err != nil {
		return "", err
	}

	body, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	return p.PublishBytes(ctx, topic, body, attributes)
}

// PublishBytes preserves canonical JSON or binary payloads without re-encoding.
func (p PubSub) PublishBytes(ctx context.Context, topic string, body []byte, attributes map[string]string) (string, error) {
	if err := ready(ctx, p.Publisher); err != nil {
		return "", err
	}
	r, err := p.Publisher.Publish(ctx, &pb.PublishRequest{Topic: topic, Messages: []*pb.PubsubMessage{{Data: body, Attributes: attributes}}})
	if err != nil {
		return "", err
	}
	if r == nil || len(r.MessageIds) != 1 || r.MessageIds[0] == "" {
		return "", fmt.Errorf("gcp: publish returned no message acknowledgement")
	}
	return r.MessageIds[0], nil
}
func (p PubSub) TopicExists(ctx context.Context, name string) (bool, error) {
	if err := ready(ctx, p.Publisher); err != nil {
		return false, err
	}
	_, err := p.Publisher.GetTopic(ctx, &pb.GetTopicRequest{Topic: name})
	return exists(err)
}
func (p PubSub) CreateTopic(ctx context.Context, name string) error {
	if err := ready(ctx, p.Publisher); err != nil {
		return err
	}
	_, err := p.Publisher.CreateTopic(ctx, &pb.Topic{Name: name})
	if status.Code(err) == codes.AlreadyExists {
		return nil
	}
	return err
}
func (p PubSub) DeleteTopic(ctx context.Context, name string) error {
	if err := ready(ctx, p.Publisher); err != nil {
		return err
	}
	return ignoreNotFound(p.Publisher.DeleteTopic(ctx, &pb.DeleteTopicRequest{Topic: name}))
}
func (p PubSub) SubscriptionExists(ctx context.Context, name string) (bool, error) {
	if err := ready(ctx, p.Subscriber); err != nil {
		return false, err
	}
	_, err := p.Subscriber.GetSubscription(ctx, &pb.GetSubscriptionRequest{Subscription: name})
	return exists(err)
}

// CreateSubscription never deletes or replaces an existing subscription/backlog.
// AlreadyExists is returned so the caller can inspect configuration explicitly.
func (p PubSub) CreateSubscription(ctx context.Context, name, topic string, push *HTTPDispatch, labels map[string]string) (*pb.Subscription, error) {
	if err := ready(ctx, p.Subscriber); err != nil {
		return nil, err
	}

	sub := &pb.Subscription{Name: name, Topic: topic, AckDeadlineSeconds: 60, ExpirationPolicy: &pb.ExpirationPolicy{}, Labels: labels}
	if push != nil {
		if push.Data != nil || (push.Method != "" && !strings.EqualFold(push.Method, "POST")) {
			return nil, fmt.Errorf("gcp: push method and payload are provider-owned")
		}
		if push.BearerToken != "" {
			return nil, fmt.Errorf("gcp: push subscription requires OIDC")
		}
		_, _, aud, err := push.prepare()
		if err != nil {
			return nil, err
		}
		sub.PushConfig = &pb.PushConfig{PushEndpoint: push.URL, AuthenticationMethod: &pb.PushConfig_OidcToken_{OidcToken: &pb.PushConfig_OidcToken{ServiceAccountEmail: push.ServiceAccount, Audience: aud}}}
	}
	return p.Subscriber.CreateSubscription(ctx, sub)
}
func (p PubSub) DeleteSubscription(ctx context.Context, name string) error {
	if err := ready(ctx, p.Subscriber); err != nil {
		return err
	}
	return ignoreNotFound(p.Subscriber.DeleteSubscription(ctx, &pb.DeleteSubscriptionRequest{Subscription: name}))
}

// Pull is a bounded batch with explicit acknowledgement ownership. Ack extends
// no lease automatically: use the actual subscription deadline and call Extend before expiry.
func (p PubSub) Pull(ctx context.Context, subscription string, max int32) ([]*pb.ReceivedMessage, error) {
	if err := ready(ctx, p.Subscriber); err != nil {
		return nil, err
	}
	if max <= 0 {
		return nil, fmt.Errorf("gcp: max messages must be positive")
	}
	r, err := p.Subscriber.Pull(ctx, &pb.PullRequest{Subscription: subscription, MaxMessages: max})
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("gcp: nil pull response")
	}
	return r.ReceivedMessages, nil
}
func (p PubSub) Ack(ctx context.Context, subscription string, ids ...string) error {
	if err := ready(ctx, p.Subscriber); err != nil {
		return err
	}
	if err := ackIDs(ids); err != nil {
		return err
	}
	return p.Subscriber.Acknowledge(ctx, &pb.AcknowledgeRequest{Subscription: subscription, AckIds: ids})
}
func (p PubSub) Nack(ctx context.Context, subscription string, ids ...string) error {
	if err := ready(ctx, p.Subscriber); err != nil {
		return err
	}
	return p.Extend(ctx, subscription, 0, ids...)
}
func (p PubSub) Extend(ctx context.Context, subscription string, seconds int32, ids ...string) error {
	if err := ready(ctx, p.Subscriber); err != nil {
		return err
	}
	if err := ackIDs(ids); err != nil {
		return err
	}
	if seconds < 0 || seconds > 600 {
		return fmt.Errorf("gcp: ack IDs and deadline in [0,600] required")
	}
	return p.Subscriber.ModifyAckDeadline(ctx, &pb.ModifyAckDeadlineRequest{Subscription: subscription, AckIds: ids, AckDeadlineSeconds: seconds})
}
