package aws

import (
	"context"
	"errors"
	"strings"
	"time"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Dispatch enqueues native SQS work. An application worker must receive, execute
// and acknowledge it. It does not make HTTP requests or mint OIDC credentials.
// Standard queues accept integral delays of 0..900 seconds. FIFO queues use
// queue-level delay only: request-level DelaySeconds must be absent.
func (a SQS) Dispatch(ctx context.Context, in *sqs.SendMessageInput, delay time.Duration) (*sqs.SendMessageOutput, error) {
	if in == nil || in.QueueUrl == nil || *in.QueueUrl == "" {
		return nil, errors.New("aws sqs: queue URL required")
	}
	if delay < 0 || delay > 900*time.Second || delay%time.Second != 0 {
		return nil, errors.New("aws sqs: delay must be integral seconds in 0..900; use Scheduler for longer delays")
	}
	if in.DelaySeconds != 0 {
		return nil, errors.New("aws sqs: set delay through Dispatch argument only")
	}
	copy := *in
	if strings.HasSuffix(*in.QueueUrl, ".fifo") {
		if delay != 0 {
			return nil, errors.New("aws sqs: FIFO per-message delay unsupported")
		}
	} else {
		copy.DelaySeconds = int32(delay / time.Second)
	}
	return a.SendMessage(ctx, &copy)
}

// Delivery retains the exact receipt handle from this receive attempt. No
// automatic acknowledgment occurs, including on cancellation or handler failure.
// SQS may redeliver even after a successful delete; consumers must be idempotent.
type Delivery struct {
	Message  types.Message
	queueURL string
	receipt  string
	client   SQS
}

func (a SQS) Receive(ctx context.Context, in *sqs.ReceiveMessageInput) ([]Delivery, error) {
	if in == nil || in.QueueUrl == nil || *in.QueueUrl == "" {
		return nil, errors.New("aws sqs: queue URL required")
	}
	out, err := a.ReceiveMessage(ctx, in)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("aws sqs: missing receive response")
	}
	deliveries := make([]Delivery, 0, len(out.Messages))
	for _, m := range out.Messages {
		if m.ReceiptHandle == nil || *m.ReceiptHandle == "" {
			return nil, errors.New("aws sqs: received message without receipt handle")
		}
		deliveries = append(deliveries, Delivery{Message: m, queueURL: *in.QueueUrl, receipt: *m.ReceiptHandle, client: a})
	}
	return deliveries, nil
}

func (d Delivery) Ack(ctx context.Context) error {
	if d.queueURL == "" || d.receipt == "" {
		return errors.New("aws sqs: delivery receipt required")
	}
	_, err := d.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: sdk.String(d.queueURL), ReceiptHandle: sdk.String(d.receipt)})
	return err
}

// Nack changes visibility; zero makes the message immediately available again.
// A nonzero value extends visibility, subject to SQS's 12-hour receive limit.
func (d Delivery) Nack(ctx context.Context, visibility time.Duration) error {
	if d.queueURL == "" || d.receipt == "" {
		return errors.New("aws sqs: delivery receipt required")
	}
	if visibility < 0 || visibility > 12*time.Hour || visibility%time.Second != 0 {
		return errors.New("aws sqs: visibility must be integral seconds in 0..43200")
	}
	_, err := d.client.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{QueueUrl: sdk.String(d.queueURL), ReceiptHandle: sdk.String(d.receipt), VisibilityTimeout: int32(visibility / time.Second)})
	return err
}
