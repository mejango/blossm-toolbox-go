package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"blossm.local/backend/dia"
	"blossm.local/backend/eventstore"
	"blossm.local/backend/projection"
	"blossm.local/backend/rpc"
	"blossm.local/backend/transport"
)

// Invocation is executed without a shell. Input is never serialized into plans.
type Invocation struct {
	Args  []string
	Dir   string
	Input []byte
}
type Runner func(context.Context, Invocation) ([]byte, error)
type Execution struct {
	Runner Runner
	HTTP   *http.Client
	Getenv func(string) string
	// OpenJWT replaces runtimeauth.Open only for isolated signer fixtures.
	OpenJWT func(context.Context, string) (*dia.JWT, func(), error)
}

func processRunner(ctx context.Context, in Invocation) ([]byte, error) {
	if len(in.Args) == 0 {
		return nil, errors.New("empty provider command")
	}
	cmd := exec.CommandContext(ctx, in.Args[0], in.Args[1:]...)
	cmd.Dir = in.Dir
	cmd.Stdin = bytes.NewReader(in.Input)
	// Responses may contain private fields. Only the executor's parsed result is returned.
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s execution failed: %w", in.Args[0], err)
	}
	return output, nil
}
func (e Execution) run(ctx context.Context, in Invocation) ([]byte, error) {
	if e.Runner != nil {
		return e.Runner(ctx, in)
	}
	return processRunner(ctx, in)
}
func (e Execution) getenv(name string) string {
	if e.Getenv != nil {
		return e.Getenv(name)
	}
	return os.Getenv(name)
}

type Action struct {
	Args     []string `json:"argv"`
	InputEnv string   `json:"stdinEnvironment,omitempty"`
}
type WorkflowPlan struct {
	Status    string   `json:"status"`
	Operation string   `json:"operation"`
	Actions   []Action `json:"actions,omitempty"`
	Endpoint  string   `json:"endpoint,omitempty"`
	BearerEnv string   `json:"bearerEnvironment,omitempty"`
	InputFile string   `json:"inputFile,omitempty"`
	Note      string   `json:"note"`
}

func printPlan(out io.Writer, p any) error {
	b, err := encode(p)
	if err != nil {
		return err
	}
	_, err = out.Write(b)
	return err
}
func runWorkflow(ctx context.Context, args []string, out, stderr io.Writer, e Execution) error {
	name := args[0]
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	config := f.String("config", "blossm.json", "strict deployment configuration")
	execute := f.Bool("execute", false, "explicitly execute provider commands or authenticated HTTP")
	dry := f.Bool("dry-run", false, "prepare offline only (default)")
	if name == "deploy" {
		update := f.Bool("update", false, "update an existing AWS service instead of creating it")
		if err := parse(f, args[1:]); err != nil {
			return err
		}
		if *execute && *dry {
			return errors.New("execute and dry-run are mutually exclusive")
		}
		c, err := ReadConfig(*config)
		if err != nil {
			return err
		}
		p, err := Prepare(c)
		if err != nil {
			return err
		}
		if *update {
			if c.Provider != "aws" {
				return errors.New("update selects an existing AWS ECS service only")
			}
			p.ReviewCommands[1] = []string{"aws", "ecs", "update-service", "--region", c.AWS.Region, "--cluster", c.AWS.Cluster, "--service", c.Name, "--task-definition", "<registered-task-definition-arn>"}
		}
		if !*execute {
			return printPlan(out, p)
		}
		if err = executeDeploy(ctx, c, p, *update, e); err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, "Provider deployment command and readiness wait completed; application health remains unverified.")
		return err
	}
	if name == "job" || name == "replay" {
		endpoint := f.String("url", "", "HTTPS operation endpoint, or HTTP on localhost/loopback IP")
		bearer := f.String("bearer-env", "BLOSSM_BEARER_TOKEN", "environment variable containing bearer token")
		input := f.String("input", "", "JSON input file; defaults to empty object")
		key := f.String("idempotency-key", "", "stable request identity (required for execution)")
		if err := parse(f, args[1:]); err != nil {
			return err
		}
		if *execute && *dry {
			return errors.New("execute and dry-run are mutually exclusive")
		}
		if _, err := ReadConfig(*config); err != nil {
			return err
		}
		u, err := url.Parse(*endpoint)
		if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(*endpoint, "#") {
			return errors.New("operation URL must have a host and no credentials, query or fragment")
		}
		ip := net.ParseIP(u.Hostname())
		loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
			return errors.New("operation URL requires HTTPS except localhost or loopback IP")
		}
		base := *u
		base.Path = ""
		base.RawPath = ""
		client, err := rpc.New(base.String(), rpc.Options{HTTP: e.HTTP})
		if err != nil {
			return err
		}
		if !envName.MatchString(*bearer) {
			return errors.New("invalid bearer environment name")
		}
		data := []byte(`{}`)
		if *input != "" {
			data, err = readJSONFile(*input)
			if err != nil {
				return err
			}
		}
		var replay transport.ProjectionReplayInput
		var projectionName string
		if name == "replay" {
			if err := eventstore.Decode(data, &replay); err != nil {
				return fmt.Errorf("invalid replay input: %w", err)
			}
			if err := replay.RunID.Validate(); err != nil {
				return err
			}
			if replay.Limit < 1 || replay.Limit > projection.MaxReplayBatch {
				return fmt.Errorf("replay limit must be1..%d", projection.MaxReplayBatch)
			}
			parts := strings.Split(u.Path, "/")
			if len(parts) != 4 || parts[0] != "" || parts[1] != "projections" || parts[3] != "replay" || strings.TrimSpace(parts[2]) == "" || strings.ContainsAny(parts[2], "\\%\x00\r\n") || parts[2] == "." || parts[2] == ".." {
				return errors.New("replay URL must name /projections/{name}/replay")
			}
			projectionName = parts[2]
		}
		p := WorkflowPlan{Status: "prepared-only", Operation: name, Endpoint: *endpoint, BearerEnv: *bearer, InputFile: *input, Note: "HTTP POST to an explicitly configured authenticated handler. No request is sent without --execute. Preserve idempotency key and payload after uncertain responses."}
		if !*execute {
			return printPlan(out, p)
		}
		token := e.getenv(*bearer)
		if token == "" || strings.ContainsAny(token, "\r\n") {
			return errors.New("bearer environment is missing or invalid")
		}
		if *key == "" {
			return errors.New("execution requires a stable idempotency-key")
		}
		if name == "replay" {
			result, err := (transport.ProjectionClient{RPC: client}).Replay(ctx, projectionName, replay, http.Header{"Authorization": {"Bearer " + token}, "Idempotency-Key": {*key}})
			if err != nil {
				return err
			}
			return printPlan(out, result)
		}
		response, err := client.Do(ctx, "POST", u.EscapedPath(), nil, json.RawMessage(data), http.Header{"Authorization": {"Bearer " + token}, "Idempotency-Key": {*key}})
		if err != nil {
			return err
		}
		_, err = out.Write(response.Body)
		return err
	}
	action := f.String("action", "", "operation: roles upload; secret create|put; queue create|update|delete")
	resource := f.String("name", "", "provider resource name or queue URL")
	input := f.String("input", "", "roles JSON file")
	destination := f.String("destination", "", "gs:// or s3:// object destination")
	secretEnv := f.String("value-env", "", "environment variable supplying secret bytes via stdin")
	rate := f.Int("rate", 100, "GCP maximum dispatches/second")
	concurrency := f.Int("concurrency", 10, "GCP concurrent dispatches")
	visibility := f.Int("visibility", 30, "AWS visibility timeout seconds")
	if err := parse(f, args[1:]); err != nil {
		return err
	}
	if *execute && *dry {
		return errors.New("execute and dry-run are mutually exclusive")
	}
	c, err := ReadConfig(*config)
	if err != nil {
		return err
	}
	p := WorkflowPlan{Status: "prepared-only", Operation: name + " " + *action, Note: "Provider argv only, no shell or IAM grants. --execute runs these operations in order and stops on the first failure; earlier operations may already have committed."}
	switch name {
	case "roles":
		if *action != "upload" {
			return errors.New("roles action must be upload")
		}
		if _, err = readJSONFile(*input); err != nil {
			return err
		}
		src, err := filepath.Abs(*input)
		if err != nil {
			return err
		}
		dest, err := url.Parse(*destination)
		scheme := "gs"
		if c.Provider == "aws" {
			scheme = "s3"
		}
		if err != nil || dest.Scheme != scheme || dest.Host == "" || dest.Path == "" || dest.Path == "/" || dest.User != nil || dest.RawQuery != "" || dest.Fragment != "" {
			return errors.New("roles destination must identify a provider bucket object")
		}
		if c.Provider == "gcp" {
			p.Actions = []Action{{Args: []string{"gcloud", "storage", "cp", src, *destination, "--project", c.GCP.Project, "--quiet"}}}
		} else {
			p.Actions = []Action{{Args: []string{"aws", "s3", "cp", src, *destination, "--region", c.AWS.Region, "--no-cli-pager"}}}
		}
	case "secret":
		if *action != "create" && *action != "put" {
			return errors.New("secret action must be create or put")
		}
		if !envName.MatchString(*secretEnv) {
			return errors.New("secret requires value-env name; values never belong in argv")
		}
		if c.Provider == "gcp" {
			if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(*resource) {
				return errors.New("invalid GCP secret ID")
			}
			if *action == "create" {
				p.Actions = append(p.Actions, Action{Args: []string{"gcloud", "secrets", "create", *resource, "--replication-policy=automatic", "--project", c.GCP.Project, "--quiet"}})
			}
			p.Actions = append(p.Actions, Action{Args: []string{"gcloud", "secrets", "versions", "add", *resource, "--data-file=-", "--project", c.GCP.Project, "--quiet"}, InputEnv: *secretEnv})
		} else {
			if *resource == "" || strings.HasPrefix(*resource, "-") || strings.ContainsAny(*resource, "\r\n\x00") {
				return errors.New("invalid AWS secret name or ARN")
			}
			op, flag := "put-secret-value", "--secret-id"
			if *action == "create" {
				op, flag = "create-secret", "--name"
			}
			p.Actions = []Action{{Args: []string{"aws", "secretsmanager", op, flag, *resource, "--secret-string", "file:///dev/stdin", "--region", c.AWS.Region, "--no-cli-pager"}, InputEnv: *secretEnv}}
		}
	case "queue":
		if *action != "create" && *action != "update" && *action != "delete" {
			return errors.New("queue action must be create, update or delete")
		}
		if c.Provider == "gcp" {
			if !resourceName.MatchString(*resource) || *rate < 1 || *concurrency < 1 {
				return errors.New("GCP queue requires valid name and positive rate/concurrency")
			}
			a := []string{"gcloud", "tasks", "queues", *action, *resource, "--location", c.GCP.Region, "--project", c.GCP.Project, "--quiet"}
			if *action != "delete" {
				a = append(a, "--max-dispatches-per-second", strconv.Itoa(*rate), "--max-concurrent-dispatches", strconv.Itoa(*concurrency))
			}
			p.Actions = []Action{{Args: a}}
		} else {
			if *visibility < 0 || *visibility > 43200 {
				return errors.New("AWS visibility must be 0-43200 seconds")
			}
			a := []string{"aws", "sqs"}
			if *action == "create" {
				if !resourceName.MatchString(*resource) {
					return errors.New("invalid queue name")
				}
				a = append(a, "create-queue", "--queue-name", *resource)
			} else {
				u, err := url.Parse(*resource)
				if err != nil || u.Scheme != "https" || u.Host != "sqs."+c.AWS.Region+".amazonaws.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
					return errors.New("invalid regional SQS URL")
				}
				op := "set-queue-attributes"
				if *action == "delete" {
					op = "delete-queue"
				}
				a = append(a, op, "--queue-url", *resource)
			}
			if *action != "delete" {
				a = append(a, "--attributes", "VisibilityTimeout="+strconv.Itoa(*visibility))
			}
			a = append(a, "--region", c.AWS.Region, "--no-cli-pager")
			p.Actions = []Action{{Args: a}}
		}
	default:
		return fmt.Errorf("unknown workflow %s", name)
	}
	if !*execute {
		return printPlan(out, p)
	}
	// Validate secret availability before the first mutation (including secret creation).
	for _, a := range p.Actions {
		if a.InputEnv != "" && e.getenv(a.InputEnv) == "" {
			return errors.New("secret value environment is empty")
		}
	}
	for _, a := range p.Actions {
		var input []byte
		if a.InputEnv != "" {
			input = []byte(e.getenv(a.InputEnv))
		}
		if _, err = e.run(ctx, Invocation{Args: a.Args, Input: input}); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(out, "Provider operations completed; remote application behavior remains unverified.")
	return err
}
func readJSONFile(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("input JSON file is required")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 || !json.Valid(data) {
		return nil, errors.New("input must be valid JSON no larger than 1 MiB")
	}
	return data, nil
}
func executeDeploy(ctx context.Context, c Config, p Plan, update bool, e Execution) error {
	dir, err := os.MkdirTemp("", "blossm-deploy-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	for name, data := range p.Artifacts {
		if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			return err
		}
	}
	if c.Provider == "gcp" {
		_, err = e.run(ctx, Invocation{Dir: dir, Args: []string{"gcloud", "run", "services", "replace", "cloud-run.json", "--project", c.GCP.Project, "--region", c.GCP.Region, "--quiet", "--format=json"}})
		return err
	}
	output, err := e.run(ctx, Invocation{Dir: dir, Args: []string{"aws", "ecs", "register-task-definition", "--region", c.AWS.Region, "--cli-input-json", "file://ecs-task.json", "--output", "json", "--no-cli-pager"}})
	if err != nil {
		return err
	}
	var registered struct {
		Task struct {
			ARN string `json:"taskDefinitionArn"`
		} `json:"taskDefinition"`
	}
	if err = json.Unmarshal(output, &registered); err != nil || !arn(registered.Task.ARN, "ecs", ":task-definition/") {
		return errors.New("task registration did not return a valid task definition ARN; no service change attempted")
	}
	if update {
		_, err = e.run(ctx, Invocation{Args: []string{"aws", "ecs", "update-service", "--region", c.AWS.Region, "--cluster", c.AWS.Cluster, "--service", c.Name, "--task-definition", registered.Task.ARN, "--no-cli-pager"}})
	} else {
		var service map[string]any
		if err = json.Unmarshal(p.Artifacts["ecs-service.json"], &service); err != nil {
			return err
		}
		service["taskDefinition"] = registered.Task.ARN
		data, err := encode(service)
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, "ecs-service.json"), data, 0600); err != nil {
			return err
		}
		_, err = e.run(ctx, Invocation{Dir: dir, Args: []string{"aws", "ecs", "create-service", "--region", c.AWS.Region, "--cli-input-json", "file://ecs-service.json", "--no-cli-pager"}})
	}
	if err != nil {
		return err
	}
	_, err = e.run(ctx, Invocation{Args: []string{"aws", "ecs", "wait", "services-stable", "--region", c.AWS.Region, "--cluster", c.AWS.Cluster, "--services", c.Name, "--no-cli-pager"}})
	return err
}
