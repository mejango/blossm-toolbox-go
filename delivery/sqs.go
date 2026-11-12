package delivery

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	cloudaws "github.com/mejango/blossm-toolbox-go/cloud/aws"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/projection"
	"github.com/mejango/blossm-toolbox-go/stores/event"
)

type SQSTarget struct {
	Queue    cloudaws.SQS
	QueueURL string
	// GroupID is required for FIFO queues and omitted for standard queues.
	GroupID string
}

func (t SQSTarget) send(ctx context.Context, body []byte) (string, error) {
	e, err := preflight(ctx, body)
	if err != nil {
		return "", err
	}
	if t.QueueURL == "" || t.Queue.Client == nil {
		return "", fmt.Errorf("SQS queue and client required")
	}
	in := &sqs.SendMessageInput{QueueUrl: sdk.String(t.QueueURL), MessageBody: sdk.String(string(body))}
	if strings.HasSuffix(t.QueueURL, ".fifo") {
		if t.GroupID == "" || len(t.GroupID) > 128 {
			return "", fmt.Errorf("FIFO group ID required (max128 bytes)")
		}
		in.MessageGroupId = sdk.String(t.GroupID)
		in.MessageDeduplicationId = sdk.String(e.ID)
	} else if t.GroupID != "" {
		return "", fmt.Errorf("group ID requires FIFO queue")
	}
	out, err := t.Queue.Dispatch(ctx, in, 0)
	if err != nil {
		return "", enqueueError(err)
	}
	if out == nil || out.MessageId == nil || *out.MessageId == "" {
		return "", enqueueError(fmt.Errorf("SQS missing acceptance"))
	}
	return *out.MessageId, nil
}

type SQSCommands struct{ Target SQSTarget }

func (q SQSCommands) Enqueue(ctx context.Context, s command.Submission) (command.Acceptance, error) {
	body, err := commandBody(s)
	if err != nil {
		return command.Acceptance{}, err
	}
	id, err := q.Target.send(ctx, body)
	return commandAcceptance(body, id, err)
}

type SQSReplay struct {
	Target SQSTarget
}

func (q SQSReplay) Enqueue(ctx context.Context, r projection.Request) error {
	body, err := replayBody(r)
	if err != nil {
		return err
	}
	_, err = q.Target.send(ctx, body)
	return err
}

type SQSPublications struct{ Target SQSTarget }

func (q SQSPublications) Publish(ctx context.Context, p eventstore.Publication) error {
	body, err := publicationBody(p)
	if err != nil {
		return err
	}
	_, err = q.Target.send(ctx, body)
	return err
}

// Poll receives one bounded provider batch and awaits each consumer serially.
// Durable terminal rejections are acknowledged and the batch continues.
// On other failure it leaves this and remaining receipts unacknowledged. Visibility
// expiry permits redelivery; configure it for the workload or renew externally.
type SQSWorker struct {
	Queue    cloudaws.SQS
	Consumer *Consumer
}

func (w SQSWorker) Poll(ctx context.Context, in *sqs.ReceiveMessageInput) (int, error) {
	if w.Consumer == nil {
		return 0, fmt.Errorf("delivery consumer required")
	}
	deliveries, err := w.Queue.Receive(ctx, in)
	if err != nil {
		return 0, err
	}
	acked := 0
	for _, delivery := range deliveries {
		if err = ctx.Err(); err != nil {
			return acked, err
		}
		if delivery.Message.Body == nil {
			return acked, ErrInvalid
		}
		if _, err = w.Consumer.Consume(ctx, []byte(*delivery.Message.Body)); err != nil {
			return acked, err
		}
		if err = delivery.Ack(ctx); err != nil {
			return acked, err
		}
		acked++
	}
	return acked, nil
}

var _ command.Queue = SQSCommands{}
var _ projection.Producer = SQSReplay{}
var _ eventstore.Publisher = SQSPublications{}
