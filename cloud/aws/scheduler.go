package aws

import (
	"context"
	"errors"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
)

// SchedulerAPI is the provider-specific injection boundary. *scheduler.Client implements it.
type SchedulerAPI interface {
	CreateSchedule(context.Context, *scheduler.CreateScheduleInput, ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error)
	GetSchedule(context.Context, *scheduler.GetScheduleInput, ...func(*scheduler.Options)) (*scheduler.GetScheduleOutput, error)
	UpdateSchedule(context.Context, *scheduler.UpdateScheduleInput, ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error)
	DeleteSchedule(context.Context, *scheduler.DeleteScheduleInput, ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error)
}

// Scheduler preserves AWS request, response, error and pagination semantics.
type Scheduler struct{ Client SchedulerAPI }

func (a Scheduler) CreateSchedule(ctx context.Context, in *scheduler.CreateScheduleInput, opts ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws scheduler.CreateSchedule: client and input required")
	}
	return a.Client.CreateSchedule(ctx, in, opts...)
}

func (a Scheduler) GetSchedule(ctx context.Context, in *scheduler.GetScheduleInput, opts ...func(*scheduler.Options)) (*scheduler.GetScheduleOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws scheduler.GetSchedule: client and input required")
	}
	return a.Client.GetSchedule(ctx, in, opts...)
}

func (a Scheduler) UpdateSchedule(ctx context.Context, in *scheduler.UpdateScheduleInput, opts ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws scheduler.UpdateSchedule: client and input required")
	}
	return a.Client.UpdateSchedule(ctx, in, opts...)
}

func (a Scheduler) DeleteSchedule(ctx context.Context, in *scheduler.DeleteScheduleInput, opts ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a.Client == nil || in == nil {
		return nil, errors.New("aws scheduler.DeleteSchedule: client and input required")
	}
	return a.Client.DeleteSchedule(ctx, in, opts...)
}

var _ SchedulerAPI = (*scheduler.Client)(nil)
