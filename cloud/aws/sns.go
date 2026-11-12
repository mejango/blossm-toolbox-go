package aws

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/sns"
)

// SNSAPI is the provider-specific injection boundary. *sns.Client implements it.
type SNSAPI interface {
	Publish(context.Context, *sns.PublishInput, ...func(*sns.Options)) (*sns.PublishOutput, error)
	CreateTopic(context.Context, *sns.CreateTopicInput, ...func(*sns.Options)) (*sns.CreateTopicOutput, error)
	GetTopicAttributes(context.Context, *sns.GetTopicAttributesInput, ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error)
	DeleteTopic(context.Context, *sns.DeleteTopicInput, ...func(*sns.Options)) (*sns.DeleteTopicOutput, error)
	Subscribe(context.Context, *sns.SubscribeInput, ...func(*sns.Options)) (*sns.SubscribeOutput, error)
	Unsubscribe(context.Context, *sns.UnsubscribeInput, ...func(*sns.Options)) (*sns.UnsubscribeOutput, error)
}

// SNS preserves AWS request, response, error and pagination semantics.
type SNS struct{ Client SNSAPI }

func (a SNS) Publish(ctx context.Context, in *sns.PublishInput, opts ...func(*sns.Options)) (*sns.PublishOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sns.Publish: client and input required")
	}
	return a.Client.Publish(ctx, in, opts...)
}

func (a SNS) CreateTopic(ctx context.Context, in *sns.CreateTopicInput, opts ...func(*sns.Options)) (*sns.CreateTopicOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sns.CreateTopic: client and input required")
	}
	return a.Client.CreateTopic(ctx, in, opts...)
}

func (a SNS) GetTopicAttributes(ctx context.Context, in *sns.GetTopicAttributesInput, opts ...func(*sns.Options)) (*sns.GetTopicAttributesOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sns.GetTopicAttributes: client and input required")
	}
	return a.Client.GetTopicAttributes(ctx, in, opts...)
}

func (a SNS) DeleteTopic(ctx context.Context, in *sns.DeleteTopicInput, opts ...func(*sns.Options)) (*sns.DeleteTopicOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sns.DeleteTopic: client and input required")
	}
	return a.Client.DeleteTopic(ctx, in, opts...)
}

func (a SNS) Subscribe(ctx context.Context, in *sns.SubscribeInput, opts ...func(*sns.Options)) (*sns.SubscribeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sns.Subscribe: client and input required")
	}
	return a.Client.Subscribe(ctx, in, opts...)
}

func (a SNS) Unsubscribe(ctx context.Context, in *sns.UnsubscribeInput, opts ...func(*sns.Options)) (*sns.UnsubscribeOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sns.Unsubscribe: client and input required")
	}
	return a.Client.Unsubscribe(ctx, in, opts...)
}

var _ SNSAPI = (*sns.Client)(nil)
