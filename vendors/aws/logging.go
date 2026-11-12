package aws

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
)

// Write preserves native batch limits/order. Partial rejection is an error and
// retains the full response so callers can retry only eligible events.
func (a Logs) Write(ctx context.Context, in *cloudwatchlogs.PutLogEventsInput) (*cloudwatchlogs.PutLogEventsOutput, error) {
	out, err := a.PutLogEvents(ctx, in)
	if err != nil {
		return out, err
	}
	if out == nil {
		return nil, errors.New("aws logs: missing response")
	}
	if out.RejectedLogEventsInfo != nil || out.RejectedEntityInfo != nil {
		return out, fmt.Errorf("aws logs: batch partially rejected: events=%+v entity=%+v", out.RejectedLogEventsInfo, out.RejectedEntityInfo)
	}
	return out, nil
}
