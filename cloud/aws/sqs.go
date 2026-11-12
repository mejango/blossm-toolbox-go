package aws

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

// SQSAPI is the provider-specific injection boundary. *sqs.Client implements it.
type SQSAPI interface {
	SendMessage(context.Context, *sqs.SendMessageInput, ...func(*sqs.Options)) (*sqs.SendMessageOutput, error)
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
	CreateQueue(context.Context, *sqs.CreateQueueInput, ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error)
	GetQueueUrl(context.Context, *sqs.GetQueueUrlInput, ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error)
	SetQueueAttributes(context.Context, *sqs.SetQueueAttributesInput, ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error)
	DeleteQueue(context.Context, *sqs.DeleteQueueInput, ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error)
}

// SQS preserves AWS request, response, error and pagination semantics.
type SQS struct{ Client SQSAPI }

func (a SQS) SendMessage(ctx context.Context, in *sqs.SendMessageInput, opts ...func(*sqs.Options)) (*sqs.SendMessageOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.SendMessage: client and input required")
	}
	return a.Client.SendMessage(ctx, in, opts...)
}

func (a SQS) ReceiveMessage(ctx context.Context, in *sqs.ReceiveMessageInput, opts ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.ReceiveMessage: client and input required")
	}
	return a.Client.ReceiveMessage(ctx, in, opts...)
}

func (a SQS) DeleteMessage(ctx context.Context, in *sqs.DeleteMessageInput, opts ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.DeleteMessage: client and input required")
	}
	return a.Client.DeleteMessage(ctx, in, opts...)
}

func (a SQS) ChangeMessageVisibility(ctx context.Context, in *sqs.ChangeMessageVisibilityInput, opts ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.ChangeMessageVisibility: client and input required")
	}
	return a.Client.ChangeMessageVisibility(ctx, in, opts...)
}

func (a SQS) CreateQueue(ctx context.Context, in *sqs.CreateQueueInput, opts ...func(*sqs.Options)) (*sqs.CreateQueueOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.CreateQueue: client and input required")
	}
	return a.Client.CreateQueue(ctx, in, opts...)
}

func (a SQS) GetQueueUrl(ctx context.Context, in *sqs.GetQueueUrlInput, opts ...func(*sqs.Options)) (*sqs.GetQueueUrlOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.GetQueueUrl: client and input required")
	}
	return a.Client.GetQueueUrl(ctx, in, opts...)
}

func (a SQS) SetQueueAttributes(ctx context.Context, in *sqs.SetQueueAttributesInput, opts ...func(*sqs.Options)) (*sqs.SetQueueAttributesOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.SetQueueAttributes: client and input required")
	}
	return a.Client.SetQueueAttributes(ctx, in, opts...)
}

func (a SQS) DeleteQueue(ctx context.Context, in *sqs.DeleteQueueInput, opts ...func(*sqs.Options)) (*sqs.DeleteQueueOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws sqs.DeleteQueue: client and input required")
	}
	return a.Client.DeleteQueue(ctx, in, opts...)
}

var _ SQSAPI = (*sqs.Client)(nil)
