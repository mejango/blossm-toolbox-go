package runtimepub

import (
	"context"
	"errors"
	"log/slog"
	"time"

	pb "cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/googleapis/gax-go/v2"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

type GCPPublisher interface {
	Publish(context.Context, *pb.PublishRequest, ...gax.CallOption) (*pb.PublishResponse, error)
	Close() error
}
type GCPSubscriber interface {
	Pull(context.Context, *pb.PullRequest, ...gax.CallOption) (*pb.PullResponse, error)
	Acknowledge(context.Context, *pb.AcknowledgeRequest, ...gax.CallOption) error
	ModifyAckDeadline(context.Context, *pb.ModifyAckDeadlineRequest, ...gax.CallOption) error
	Close() error
}

// NewGCP takes ownership of existing official GAPIC clients on success. It does
// not discover credentials or contact the provider during construction.
func NewGCP(publisher GCPPublisher, subscriber GCPSubscriber, topic, subscription string, opts Options) (*Runtime, error) {
	if missing(publisher) || missing(subscriber) || topic == "" || subscription == "" {
		return nil, errors.New("GCP publisher, subscriber, topic and subscription required")
	}
	opts, err := opts.defaults()
	if err != nil {
		return nil, err
	}
	r := newRuntime()
	r.closeClients = func() error { return errors.Join(subscriber.Close(), publisher.Close()) }
	r.publish = func(ctx context.Context, data []byte, id string) error {
		out, err := publisher.Publish(ctx, &pb.PublishRequest{Topic: topic, Messages: []*pb.PubsubMessage{{Data: data, Attributes: map[string]string{"eventId": id}}}})
		if err != nil {
			return err
		}
		if out == nil || len(out.MessageIds) != 1 || out.MessageIds[0] == "" {
			return errors.New("Pub/Sub acknowledgment missing")
		}
		return nil
	}
	r.consume = func(ctx context.Context, handler eventstore.Publisher, logger *slog.Logger) {
		for ctx.Err() == nil {
			pull, cancel := context.WithTimeout(ctx, opts.PullTimeout)
			out, err := subscriber.Pull(pull, &pb.PullRequest{Subscription: subscription, MaxMessages: 1})
			cancel()
			if err != nil || out == nil {
				if ctx.Err() == nil {
					logger.Error("publication pull failed", "pending", true)
					pause(ctx, opts.RetryDelay)
				}
				continue
			}
			if len(out.ReceivedMessages) == 0 {
				pause(ctx, opts.RetryDelay)
				continue
			}
			for _, m := range out.ReceivedMessages {
				if ctx.Err() != nil {
					return
				}
				if m == nil || m.AckId == "" {
					logger.Error("publication receipt missing", "pending", true)
					pause(ctx, opts.RetryDelay)
					continue
				}
				ackID := m.AckId
				leaseStart := time.Now()
				// Reset this message's deadline instead of assuming the subscription's
				// configured value. Failure leaves the delivery untouched.
				lease, stop := context.WithTimeout(ctx, opts.AckTimeout)
				err = subscriber.ModifyAckDeadline(lease, &pb.ModifyAckDeadlineRequest{Subscription: subscription, AckIds: []string{ackID}, AckDeadlineSeconds: int32(deliveryLease / time.Second)})
				stop()
				if err != nil {
					logger.Error("publication lease failed", "pending", true)
					pause(ctx, opts.RetryDelay)
					continue
				}
				if m.Message == nil {
					err = errors.New("publication message missing")
				} else {
					attribute, present := m.Message.Attributes["eventId"]
					id, identityErr := eventID(attribute, present)
					work, stop := context.WithDeadline(ctx, leaseStart.Add(deliveryLease-opts.AckTimeout))
					err = identityErr
					if err == nil {
						err = handle(work, opts.HandleTimeout, handler, m.Message.Data, id)
					}
					stop()
				}
				lease, release := context.WithDeadline(ctx, leaseStart.Add(deliveryLease))
				ack, stop := context.WithTimeout(lease, opts.AckTimeout)
				if err == nil {
					err = subscriber.Acknowledge(ack, &pb.AcknowledgeRequest{Subscription: subscription, AckIds: []string{ackID}})
				} else {
					err = errors.Join(err, subscriber.ModifyAckDeadline(ack, &pb.ModifyAckDeadlineRequest{Subscription: subscription, AckIds: []string{ackID}, AckDeadlineSeconds: 0}))
				}
				stop()
				release()
				if err != nil {
					if ctx.Err() == nil {
						logger.Error("publication consumption failed", "pending", true)
						pause(ctx, opts.RetryDelay)
					}
				}
			}
		}
	}
	return r, nil
}
