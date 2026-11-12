package runtimepub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"blossm.local/backend/eventstore"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snsTypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

type SNSPublisher interface {
	Publish(context.Context, *sns.PublishInput, ...func(*sns.Options)) (*sns.PublishOutput, error)
}
type SQSSubscriber interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
}

// NewAWS accepts official SDK v2 clients. Credentials and their refresh chain
// remain caller-owned. SDK clients have no Close method; Runtime joins its users.
func NewAWS(publisher SNSPublisher, subscriber SQSSubscriber, topic, queue string, opts Options) (*Runtime, error) {
	if missing(publisher) || missing(subscriber) || topic == "" || queue == "" {
		return nil, errors.New("SNS publisher, SQS subscriber, topic and queue required")
	}
	opts, err := opts.defaults()
	if err != nil {
		return nil, err
	}
	r := newRuntime()
	r.publish = func(ctx context.Context, data []byte, id string) error {
		out, err := publisher.Publish(ctx, &sns.PublishInput{TopicArn: aws.String(topic), Message: aws.String(string(data)), MessageAttributes: map[string]snsTypes.MessageAttributeValue{"eventId": {DataType: aws.String("String"), StringValue: aws.String(id)}}})
		if err != nil {
			return err
		}
		if out == nil || aws.ToString(out.MessageId) == "" {
			return errors.New("SNS acknowledgment missing")
		}
		return nil
	}
	r.consume = func(ctx context.Context, handler eventstore.Publisher, logger *slog.Logger) {
		// Leave time for the HTTP response after long polling. Short test/operational
		// deadlines use short polling, never a longer wait than the request budget.
		wait := int32(min(20, max(0, int64(opts.PullTimeout/time.Second)-1)))
		for ctx.Err() == nil {
			leaseStart := time.Now()
			pull, cancel := context.WithTimeout(ctx, opts.PullTimeout)
			out, err := subscriber.ReceiveMessage(pull, &sqs.ReceiveMessageInput{QueueUrl: aws.String(queue), MaxNumberOfMessages: 1, WaitTimeSeconds: wait, VisibilityTimeout: int32(deliveryLease / time.Second), MessageAttributeNames: []string{"eventId"}})
			cancel()
			if err != nil || out == nil {
				if ctx.Err() == nil {
					logger.Error("SQS receive failed", "pending", true)
					pause(ctx, opts.RetryDelay)
				}
				continue
			}
			if len(out.Messages) == 0 {
				pause(ctx, opts.RetryDelay)
				continue
			}
			for _, m := range out.Messages {
				if ctx.Err() != nil {
					return
				}
				receipt := aws.ToString(m.ReceiptHandle)
				if receipt == "" {
					logger.Error("SQS receipt missing", "pending", true)
					pause(ctx, opts.RetryDelay)
					continue
				}
				attribute, present := m.MessageAttributes["eventId"]
				id, err := eventID(aws.ToString(attribute.StringValue), present)
				if present && aws.ToString(attribute.DataType) != "String" {
					err = errors.New("invalid SQS event identity type")
				}
				var body []byte
				if err == nil {
					body, id, err = awsNotice([]byte(aws.ToString(m.Body)), topic, id)
				}
				if err == nil {
					work, stop := context.WithDeadline(ctx, leaseStart.Add(deliveryLease-opts.AckTimeout))
					err = handle(work, opts.HandleTimeout, handler, body, id)
					stop()
				}
				if err == nil {
					lease, release := context.WithDeadline(ctx, leaseStart.Add(deliveryLease))
					ack, stop := context.WithTimeout(lease, opts.AckTimeout)
					_, err = subscriber.DeleteMessage(ack, &sqs.DeleteMessageInput{QueueUrl: aws.String(queue), ReceiptHandle: aws.String(receipt)})
					stop()
					release()
				}
				// Failed/timed-out work retains its receipt and the 60-second visibility
				// lease. The queue's redrive policy, not this process, owns poison messages.
				if err != nil && ctx.Err() == nil {
					logger.Error("SQS consumption failed", "pending", true)
					pause(ctx, opts.RetryDelay)
				}
			}
		}
	}
	return r, nil
}
func awsNotice(body []byte, topic, id string) ([]byte, string, error) {
	canonical, err := eventstore.Canonical(body)
	if err != nil {
		return nil, "", errors.New("invalid SQS notice encoding")
	}
	var envelope struct {
		Type              string
		TopicArn          string
		Message           string
		MessageAttributes map[string]struct{ Type, Value string }
	}
	if err := json.Unmarshal(canonical, &envelope); err != nil {
		return nil, "", errors.New("invalid SQS notice encoding")
	}
	// Raw delivery is a Publication object; a Message field is reserved for SNS.
	if envelope.Type != "" || envelope.Message != "" || envelope.TopicArn != "" {
		if envelope.Type != "Notification" || envelope.TopicArn != topic || envelope.Message == "" {
			return nil, "", errors.New("invalid SNS notification envelope")
		}
		if value, ok := envelope.MessageAttributes["eventId"]; ok {
			if value.Type != "String" || value.Value == "" || (id != "" && id != value.Value) {
				return nil, "", errors.New("invalid SNS event identity")
			}
			id = value.Value
		}
		return []byte(envelope.Message), id, nil
	}
	return body, id, nil
}
