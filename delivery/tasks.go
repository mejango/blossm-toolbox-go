package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mejango/blossm-toolbox-go/cloud/gcp"
	"github.com/mejango/blossm-toolbox-go/procedures/command"
	"github.com/mejango/blossm-toolbox-go/procedures/projection"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type TasksTarget struct {
	Tasks    gcp.Tasks
	Queue    string
	Dispatch gcp.HTTPDispatch
}

func (t TasksTarget) send(ctx context.Context, body []byte) (string, error) {
	e, err := preflight(ctx, body)
	if err != nil {
		return "", err
	}
	if t.Queue == "" || t.Tasks.Client == nil {
		return "", fmt.Errorf("tasks queue and client required")
	}
	dispatch := t.Dispatch
	dispatch.Method = "POST"
	dispatch.Data = json.RawMessage(body)
	targetID, err := eventstore.Digest([]string{"blossm.delivery.task.v1", dispatch.URL, dispatch.ServiceAccount, dispatch.Audience, e.ID})
	if err != nil {
		return "", err
	}
	name := strings.TrimSuffix(t.Queue, "/") + "/tasks/" + targetID
	task, err := t.Tasks.Enqueue(ctx, t.Queue, name, dispatch, time.Time{})
	// Content-derived names make an AlreadyExists acknowledgment exact to this
	// envelope. Provider retention is finite; durable consumers own deduplication.
	if status.Code(err) == codes.AlreadyExists {
		return name, nil
	}
	if err != nil {
		return "", enqueueError(err)
	}
	if task == nil || task.Name != name {
		return "", enqueueError(fmt.Errorf("tasks missing matching acceptance"))
	}
	return name, nil
}

type TasksCommands struct{ Target TasksTarget }

func (q TasksCommands) Enqueue(ctx context.Context, s command.Submission) (command.Acceptance, error) {
	body, err := commandBody(s)
	if err != nil {
		return command.Acceptance{}, err
	}
	id, err := q.Target.send(ctx, body)
	return commandAcceptance(body, id, err)
}

type TasksReplay struct {
	Target TasksTarget
}

func (q TasksReplay) Enqueue(ctx context.Context, r projection.Request) error {
	body, err := replayBody(r)
	if err != nil {
		return err
	}
	_, err = q.Target.send(ctx, body)
	return err
}

type TasksPublications struct{ Target TasksTarget }

func (q TasksPublications) Publish(ctx context.Context, p eventstore.Publication) error {
	body, err := publicationBody(p)
	if err != nil {
		return err
	}
	_, err = q.Target.send(ctx, body)
	return err
}

var _ command.Queue = TasksCommands{}
var _ projection.Producer = TasksReplay{}
var _ eventstore.Publisher = TasksPublications{}
