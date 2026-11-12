package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

// Plan is a review artifact. It deliberately has no executor or deployment-success state.
type Plan struct {
	Version             int                        `json:"version"`
	Status              string                     `json:"status"`
	Provider            string                     `json:"provider"`
	Artifacts           map[string]json.RawMessage `json:"artifacts"`
	Prerequisites       []string                   `json:"prerequisites"`
	PreparationCommands [][]string                 `json:"preparationCommands,omitempty"`
	ReviewCommands      [][]string                 `json:"reviewCommands"`
}

func encode(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}
func Prepare(c Config) (Plan, error) {
	if err := c.Validate(); err != nil {
		return Plan{}, err
	}
	p := Plan{Version: 1, Status: "prepared-only", Provider: c.Provider, Artifacts: map[string]json.RawMessage{}, Prerequisites: []string{
		fmt.Sprintf("Build and push the reviewed image containing /%s separately; replace the example image with your registry reference, preferably a digest.", c.Runtime),
		"Provision database with TLS, network reachability and appropriate least-privilege credentials; configure its selected namespace.",
		"Provision all secret references and grant the platform identity permission to read only the required secrets.",
		"Provision signing secrets for the selected runtimeauth mode: local PKCS8 P256 JWT_PRIVATE_KEY, or cloud KMS key plus pinned PKIX P256 JWT_PUBLIC_KEY. Provision separate base64 32-byte DIA_HMAC_KEY and DIA_ENCRYPTION_KEY; configure narrow signer and publication permissions.",
		"Before execution, define retry limits, retention, poison-message quarantine/redrive and operator recovery. Provision AWS SQS dead-letter/redrive policy separately, including FIFO blocking behavior; for GCP Tasks review queue retry policy and recovery/inspection procedures. Malformed or untrusted delivery messages are not acknowledged as successful. This plan does not provision a DLQ or redrive policy.",
		"Review application authentication and public ingress before deployment. This plan creates no IAM role bindings, database, queue, topic or DNS resource; Cloud Run invocation checks follow the explicit selected mode.",
	}}
	secrets := map[string]Secret{c.DatabaseEnv(): c.Database.Secret}
	for k, v := range c.Secrets {
		secrets[k] = v
	}
	names := make([]string, 0, len(secrets))
	for k := range secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	runtime := c.RuntimeEnv()
	keys := make([]string, 0, len(runtime))
	for k := range runtime {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if c.Provider == "gcp" {
		env := []any{}
		for _, k := range keys {
			env = append(env, map[string]any{"name": k, "value": runtime[k]})
		}
		for _, k := range names {
			env = append(env, map[string]any{"name": k, "valueFrom": map[string]any{"secretKeyRef": map[string]string{"name": secrets[k].Reference, "key": secrets[k].Version}}})
		}
		// Cloud Run injects PORT from containerPort; explicit PORT env is forbidden there.
		service := map[string]any{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": map[string]any{"name": c.Name, "namespace": c.GCP.Project, "annotations": map[string]string{"run.googleapis.com/ingress": "internal-and-cloud-load-balancing", "run.googleapis.com/invoker-iam-disabled": strconv.FormatBool(c.GCP.Invocation == "application")}}, "spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]string{"autoscaling.knative.dev/maxScale": "10", "autoscaling.knative.dev/minScale": "1", "run.googleapis.com/cpu-throttling": "false"}}, "spec": map[string]any{"serviceAccountName": c.GCP.ServiceAccount, "containerConcurrency": 80, "timeoutSeconds": 300, "containers": []any{map[string]any{"image": c.Image, "command": []string{"/" + c.Runtime}, "ports": []any{map[string]any{"containerPort": c.Port}}, "env": env, "resources": map[string]any{"limits": map[string]string{"cpu": "1", "memory": "512Mi"}}}}}}}}
		b, err := encode(service)
		if err != nil {
			return p, err
		}
		p.Artifacts["cloud-run.json"] = b
		p.Prerequisites = append(p.Prerequisites, "Cloud Run keeps at least one instance and CPU allocated between requests for autonomous publication/consumer polling. This assumes instance-based billing and incurs idle-instance cost.",
			"Enable Cloud Run and Secret Manager APIs. Provision the service account and Artifact Registry image access; add VPC connectivity if the database is private.", "Ingress defaults to internal and Cloud Load Balancing. Provision the load balancer deliberately. No allUsers role binding is generated; the explicit invocation mode controls the provider IAM check.")
		if c.GCP.Invocation == "application" {
			p.Prerequisites = append(p.Prerequisites, "REVIEW: selected application invocation mode explicitly disables Cloud Run Invoker IAM in this manifest (invoker-iam-disabled=true). Container authentication remains mandatory on protected routes. Review ingress/edge restrictions and organization policy before any --execute; no live permission was changed by preparation.")
		} else {
			p.Prerequisites = append(p.Prerequisites, "REVIEW: selected iam invocation mode explicitly keeps Cloud Run Invoker IAM enabled. Application JWT alone cannot pass it. A trusted authenticated edge/client must send a Google ID token in X-Serverless-Authorization and application JWT in Authorization; configure exact Cloud Run audience and existing invoker permission separately. Built-in application bearer clients do not synthesize that extra invocation token.")
		}
		p.ReviewCommands = [][]string{{"gcloud", "run", "services", "replace", "cloud-run.json", "--project", c.GCP.Project, "--region", c.GCP.Region}}
	} else {
		env := []any{map[string]string{"name": "PORT", "value": strconv.Itoa(c.Port)}}
		for _, k := range keys {
			env = append(env, map[string]string{"name": k, "value": runtime[k]})
		}
		refs := []any{}
		for _, k := range names {
			refs = append(refs, map[string]string{"name": k, "valueFrom": secrets[k].Reference})
		}
		task := map[string]any{"family": c.Name, "networkMode": "awsvpc", "requiresCompatibilities": []string{"FARGATE"}, "cpu": "256", "memory": "512", "executionRoleArn": c.AWS.ExecutionRole, "taskRoleArn": c.AWS.TaskRole, "runtimePlatform": map[string]string{"operatingSystemFamily": "LINUX", "cpuArchitecture": "X86_64"}, "containerDefinitions": []any{map[string]any{"name": c.Name, "image": c.Image, "entryPoint": []string{"/" + c.Runtime}, "essential": true, "readonlyRootFilesystem": true, "user": "65532:65532", "portMappings": []any{map[string]any{"containerPort": c.Port, "protocol": "tcp", "appProtocol": "http"}}, "environment": env, "secrets": refs, "logConfiguration": map[string]any{"logDriver": "awslogs", "options": map[string]string{"awslogs-group": c.AWS.LogGroup, "awslogs-region": c.AWS.Region, "awslogs-stream-prefix": c.Name}}}}}
		service := map[string]any{"cluster": c.AWS.Cluster, "serviceName": c.Name, "taskDefinition": c.Name, "desiredCount": 1, "launchType": "FARGATE", "platformVersion": "1.4.0", "deploymentConfiguration": map[string]any{"deploymentCircuitBreaker": map[string]bool{"enable": true, "rollback": true}}, "networkConfiguration": map[string]any{"awsvpcConfiguration": map[string]any{"subnets": c.AWS.Subnets, "securityGroups": c.AWS.SecurityGroups, "assignPublicIp": "DISABLED"}}, "loadBalancers": []any{map[string]any{"targetGroupArn": c.AWS.TargetGroupARN, "containerName": c.Name, "containerPort": c.Port}}}
		for name, v := range map[string]any{"ecs-task.json": task, "ecs-service.json": service} {
			b, err := encode(v)
			if err != nil {
				return p, err
			}
			p.Artifacts[name] = b
		}
		queueAttributes, err := encode(map[string]string{"MaximumMessageSize": strconv.Itoa(c.Publication.MaximumMessageSize)})
		if err != nil {
			return p, err
		}
		subscription, err := encode(map[string]any{"TopicArn": c.Publication.Topic, "Protocol": "sqs", "Endpoint": c.Publication.QueueARN, "Attributes": map[string]string{"RawMessageDelivery": "true"}, "ReturnSubscriptionArn": true})
		if err != nil {
			return p, err
		}
		p.Artifacts["sqs-publication-attributes.json"] = queueAttributes
		p.Artifacts["sns-publication-subscription.json"] = subscription
		p.PreparationCommands = [][]string{
			{"aws", "sqs", "set-queue-attributes", "--queue-url", c.Publication.QueueURL, "--attributes", "file://sqs-publication-attributes.json", "--region", c.AWS.Region, "--no-cli-pager"},
			{"aws", "sns", "subscribe", "--cli-input-json", "file://sns-publication-subscription.json", "--region", c.AWS.Region, "--no-cli-pager"},
		}
		p.Prerequisites = append(p.Prerequisites, "Before deployment, review and explicitly apply publication preparation commands to the existing queue/topic. Require raw SNS-to-SQS delivery and MaximumMessageSize >=262144. Existing subscriptions must also have RawMessageDelivery=true verified; subscribe output alone does not prove delivery. Provision narrowly scoped topic-to-queue policy separately; no IAM policy is generated or broadened.", "Complete Publication limit is245760bytes (240KiB); plus77bytes for eventId String attribute fits SNS262144. Envelope decoding compatibility does not extend that size guarantee. Preparation commands are not automatically run by deploy --execute.")
		p.Prerequisites = append(p.Prerequisites, "Provision ECS cluster, execution/task roles, CloudWatch log group and private subnet egress or VPC endpoints for image/secret/log access.", "Provision an ALB HTTP target group with target type ip, listener and ingress security groups. Application listens on 0.0.0.0:PORT; align the target-group health-check path with the selected executable.", "Register the task definition first, then replace ecs-service.json taskDefinition with the exact returned ARN/revision. Family-only lookup is not an immutable deployment. Existing services require update-service, not create-service.")
		p.ReviewCommands = [][]string{{"aws", "ecs", "register-task-definition", "--region", c.AWS.Region, "--cli-input-json", "file://ecs-task.json"}, {"aws", "ecs", "create-service", "--region", c.AWS.Region, "--cli-input-json", "file://ecs-service.json"}}
	}
	return p, nil
}

// WritePlan refuses to replace existing output, so partial generation cannot corrupt a prior plan.
func WritePlan(dir string, p Plan) error {
	parent := filepath.Dir(filepath.Clean(dir))
	if err := os.MkdirAll(parent, 0755); err != nil {
		return err
	}
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("output already exists: %s", dir)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.MkdirTemp(parent, ".blossm-plan-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for name, data := range p.Artifacts {
		if filepath.Base(name) != name {
			return fmt.Errorf("invalid artifact name %q", name)
		}
		if err = os.WriteFile(filepath.Join(tmp, name), data, 0644); err != nil {
			return err
		}
	}
	data, err := encode(p)
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(tmp, "plan.json"), data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}
