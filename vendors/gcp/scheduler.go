package gcp

import (
	"context"
	"fmt"

	scheduler "cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"github.com/googleapis/gax-go/v2"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

type SchedulerAPI interface {
	CreateJob(context.Context, *scheduler.CreateJobRequest, ...gax.CallOption) (*scheduler.Job, error)
	GetJob(context.Context, *scheduler.GetJobRequest, ...gax.CallOption) (*scheduler.Job, error)
	UpdateJob(context.Context, *scheduler.UpdateJobRequest, ...gax.CallOption) (*scheduler.Job, error)
	DeleteJob(context.Context, *scheduler.DeleteJobRequest, ...gax.CallOption) error
}
type Scheduler struct{ Client SchedulerAPI }
type Schedule struct {
	Name, Cron, TimeZone string
	Target               HTTPDispatch
}

func (s Schedule) job() (*scheduler.Job, error) {
	if s.Name == "" || s.Cron == "" || s.TimeZone == "" {
		return nil, fmt.Errorf("gcp: schedule name, cron and time zone are required")
	}
	if s.Target.BearerToken != "" {
		return nil, fmt.Errorf("gcp: Scheduler requires OIDC authentication")
	}
	body, method, aud, err := s.Target.prepare()
	if err != nil {
		return nil, err
	}
	return &scheduler.Job{Name: s.Name, Schedule: s.Cron, TimeZone: s.TimeZone, Target: &scheduler.Job_HttpTarget{HttpTarget: &scheduler.HttpTarget{Uri: s.Target.URL, HttpMethod: scheduler.HttpMethod(scheduler.HttpMethod_value[method]), Headers: map[string]string{"Content-Type": "application/json"}, Body: body, AuthorizationHeader: &scheduler.HttpTarget_OidcToken{OidcToken: &scheduler.OidcToken{ServiceAccountEmail: s.Target.ServiceAccount, Audience: aud}}}}}, nil
}
func (s Scheduler) Create(ctx context.Context, parent string, spec Schedule) (*scheduler.Job, error) {
	if err := ready(ctx, s.Client); err != nil {
		return nil, err
	}
	j, err := spec.job()
	if err != nil {
		return nil, err
	}
	return s.Client.CreateJob(ctx, &scheduler.CreateJobRequest{Parent: parent, Job: j})
}
func (s Scheduler) Update(ctx context.Context, spec Schedule) (*scheduler.Job, error) {
	if err := ready(ctx, s.Client); err != nil {
		return nil, err
	}
	j, err := spec.job()
	if err != nil {
		return nil, err
	}
	return s.Client.UpdateJob(ctx, &scheduler.UpdateJobRequest{Job: j, UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"schedule", "time_zone", "http_target"}}})
}
func (s Scheduler) Exists(ctx context.Context, name string) (bool, error) {
	if err := ready(ctx, s.Client); err != nil {
		return false, err
	}
	_, err := s.Client.GetJob(ctx, &scheduler.GetJobRequest{Name: name})
	return exists(err)
}
func (s Scheduler) Delete(ctx context.Context, name string) error {
	if err := ready(ctx, s.Client); err != nil {
		return err
	}
	return ignoreNotFound(s.Client.DeleteJob(ctx, &scheduler.DeleteJobRequest{Name: name}))
}
