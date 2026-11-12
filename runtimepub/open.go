package runtimepub

import (
	"context"
	"errors"
	"os"

	pubsub "cloud.google.com/go/pubsub/v2/apiv1"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/mejango/blossm-toolbox-go/eventstore"
)

// Open owns credential discovery and construction. Tests inject existing SDK
// clients through NewGCP/NewAWS; the official AWS default credential chain is
// deliberately not reimplemented here. The returned close cancels and joins
// runtime operations before closing clients; callers still join outer workers.
func Open(ctx context.Context, provider string) (eventstore.Publisher, Consumer, func(), error) {
	topic := os.Getenv("PUBLICATION_TOPIC")
	if topic == "" {
		return nil, nil, nil, errors.New("PUBLICATION_TOPIC required")
	}
	var runtime *Runtime
	switch provider {
	case "gcp":
		subscription := os.Getenv("PUBLICATION_SUBSCRIPTION")
		if subscription == "" {
			return nil, nil, nil, errors.New("PUBLICATION_SUBSCRIPTION required")
		}
		publisher, err := pubsub.NewTopicAdminClient(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		subscriber, err := pubsub.NewSubscriptionAdminClient(ctx)
		if err != nil {
			_ = publisher.Close()
			return nil, nil, nil, err
		}
		runtime, err = NewGCP(publisher, subscriber, topic, subscription, Options{})
		if err != nil {
			_ = subscriber.Close()
			_ = publisher.Close()
			return nil, nil, nil, err
		}
	case "aws":
		queue := os.Getenv("PUBLICATION_QUEUE_URL")
		if queue == "" {
			return nil, nil, nil, errors.New("PUBLICATION_QUEUE_URL required")
		}
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		runtime, err = NewAWS(sns.NewFromConfig(cfg), sqs.NewFromConfig(cfg), topic, queue, Options{})
		if err != nil {
			return nil, nil, nil, err
		}
	default:
		return nil, nil, nil, errors.New("PUBLISHER must be gcp or aws")
	}
	return runtime, runtime.Consume, func() { _ = runtime.Close() }, nil
}
