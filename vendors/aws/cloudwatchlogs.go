package aws

import (
	"context"
	"errors"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// LogsAPI is the provider-specific injection boundary. *cloudwatchlogs.Client implements it.
type LogsAPI interface {
	CreateLogGroup(context.Context, *cloudwatchlogs.CreateLogGroupInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error)
	CreateLogStream(context.Context, *cloudwatchlogs.CreateLogStreamInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogStreamOutput, error)
	PutLogEvents(context.Context, *cloudwatchlogs.PutLogEventsInput, ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error)
}

// Logs preserves AWS request, response, error and pagination semantics.
type Logs struct{ Client LogsAPI }

func (a Logs) CreateLogGroup(ctx context.Context, in *cloudwatchlogs.CreateLogGroupInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogGroupOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws cloudwatchlogs.CreateLogGroup: client and input required")
	}
	return a.Client.CreateLogGroup(ctx, in, opts...)
}

func (a Logs) CreateLogStream(ctx context.Context, in *cloudwatchlogs.CreateLogStreamInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.CreateLogStreamOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws cloudwatchlogs.CreateLogStream: client and input required")
	}
	return a.Client.CreateLogStream(ctx, in, opts...)
}

func (a Logs) PutLogEvents(ctx context.Context, in *cloudwatchlogs.PutLogEventsInput, opts ...func(*cloudwatchlogs.Options)) (*cloudwatchlogs.PutLogEventsOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws cloudwatchlogs.PutLogEvents: client and input required")
	}
	return a.Client.PutLogEvents(ctx, in, opts...)
}

var _ LogsAPI = (*cloudwatchlogs.Client)(nil)
