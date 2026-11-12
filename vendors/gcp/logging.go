package gcp

import (
	"context"
	"fmt"
	"time"

	pb "cloud.google.com/go/logging/apiv2/loggingpb"
	"github.com/googleapis/gax-go/v2"
	mr "google.golang.org/genproto/googleapis/api/monitoredres"
	lt "google.golang.org/genproto/googleapis/logging/type"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type LoggingAPI interface {
	WriteLogEntries(context.Context, *pb.WriteLogEntriesRequest, ...gax.CallOption) (*pb.WriteLogEntriesResponse, error)
}
type Logging struct {
	Client   LoggingAPI
	LogName  string
	Resource *mr.MonitoredResource
	Labels   map[string]string
}

// Write synchronously waits for the API response, so shutdown does not silently
// drop a background log buffer. Context and severity belong to the caller.
func (l Logging) Write(ctx context.Context, severity lt.LogSeverity, insertID string, at time.Time, fields map[string]any) error {
	if err := ready(ctx, l.Client); err != nil {
		return err
	}
	if l.LogName == "" || l.Resource == nil {
		return fmt.Errorf("gcp: log name and monitored resource required")
	}
	payload, err := structpb.NewStruct(fields)
	if err != nil {
		return err
	}
	entry := &pb.LogEntry{Severity: severity, InsertId: insertID, Payload: &pb.LogEntry_JsonPayload{JsonPayload: payload}}
	if !at.IsZero() {
		entry.Timestamp = timestamppb.New(at)
		if err := entry.Timestamp.CheckValid(); err != nil {
			return err
		}
	}
	_, err = l.Client.WriteLogEntries(ctx, &pb.WriteLogEntriesRequest{LogName: l.LogName, Resource: l.Resource, Labels: l.Labels, Entries: []*pb.LogEntry{entry}})
	return err
}
