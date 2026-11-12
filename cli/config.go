// Package cli owns offline project configuration and deployment preparation.
package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
)

const ConfigVersion = 1

// Config contains resource identifiers, never database credentials or private keys.
type Config struct {
	Version           int               `json:"version"`
	Runtime           string            `json:"runtime"`
	Service           string            `json:"service,omitempty"`
	InternalSubject   string            `json:"internalSubject,omitempty"`
	IdentityNamespace string            `json:"identityNamespace,omitempty"`
	Signing           Signing           `json:"signing"`
	Name              string            `json:"name"`
	Network           string            `json:"network"`
	Provider          string            `json:"provider"`
	Image             string            `json:"image"`
	Port              int               `json:"port"`
	Local             bool              `json:"local"`
	Database          Database          `json:"database"`
	Publication       Publication       `json:"publication"`
	Secrets           map[string]Secret `json:"secrets,omitempty"`
	GCP               *GCP              `json:"gcp,omitempty"`
	AWS               *AWS              `json:"aws,omitempty"`
	Delivery          *Delivery         `json:"delivery,omitempty"`
	Peers             *Peers            `json:"peers,omitempty"`
}
type Signing struct {
	Provider string `json:"provider"`
	KMSKey   string `json:"kmsKey,omitempty"`
}

type Publication struct {
	Topic              string `json:"topic"`
	Subscription       string `json:"subscription,omitempty"`
	QueueURL           string `json:"queueURL,omitempty"`
	QueueARN           string `json:"queueARN,omitempty"`
	RawMessageDelivery bool   `json:"rawMessageDelivery,omitempty"`
	MaximumMessageSize int    `json:"maximumMessageSize,omitempty"`
}

type Database struct {
	Driver    string `json:"driver"`
	Namespace string `json:"namespace"`
	Secret    Secret `json:"secret"`
}
type Secret struct {
	Reference string `json:"reference"`
	Version   string `json:"version,omitempty"`
}
type GCP struct {
	Project        string `json:"project"`
	Region         string `json:"region"`
	ServiceAccount string `json:"serviceAccount"`
	Invocation     string `json:"invocation"`
}
type AWS struct {
	Region         string   `json:"region"`
	Cluster        string   `json:"cluster"`
	ExecutionRole  string   `json:"executionRole"`
	TaskRole       string   `json:"taskRole"`
	Subnets        []string `json:"subnets"`
	SecurityGroups []string `json:"securityGroups"`
	LogGroup       string   `json:"logGroup"`
	TargetGroupARN string   `json:"targetGroupARN"`
}

var resourceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,48}[a-z0-9]$`)
var namespaceName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
var regionName = regexp.MustCompile(`^[a-z][a-z0-9-]+[0-9]$`)
var imageName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/@-]+$`)

func ReadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	return DecodeConfig(f)
}
func DecodeConfig(r io.Reader) (Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20+1))
	if err != nil {
		return Config{}, err
	}
	if len(data) > 1<<20 {
		return Config{}, errors.New("configuration exceeds 1 MiB")
	}
	// Reject duplicate keys before struct decoding, which otherwise silently keeps the last value.
	d := json.NewDecoder(bytes.NewReader(data))
	if err = uniqueJSON(d); err != nil {
		return Config{}, fmt.Errorf("invalid configuration JSON: %w", err)
	}
	if _, err = d.Token(); err != io.EOF {
		return Config{}, errors.New("configuration must contain exactly one JSON value")
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	var c Config
	if err = d.Decode(&c); err != nil {
		return c, fmt.Errorf("invalid configuration: %w", err)
	}
	return c, c.Validate()
}
func uniqueJSON(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			s, ok := key.(string)
			if !ok {
				return errors.New("expected object key")
			}
			if seen[s] {
				return fmt.Errorf("duplicate key %q", s)
			}
			seen[s] = true
			if err = uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = d.Token()
	return err
}
func (c Config) Validate() error {
	if c.Version != ConfigVersion {
		return fmt.Errorf("version must be %d", ConfigVersion)
	}
	if !resourceName.MatchString(c.Name) {
		return errors.New("name must be 2-50 lowercase letters, digits or hyphens, starting with a letter and ending alphanumeric")
	}
	if c.Network == "" || strings.ContainsAny(c.Network, " \t\r\n\x00") {
		return errors.New("network must be a nonempty identifier without whitespace")
	}
	if !validImage(c.Image) {
		return errors.New("image must have an explicit non-latest tag or sha256 digest")
	}
	if c.Port < 1024 || c.Port > 65535 {
		return errors.New("port must be between 1024 and 65535")
	}
	if c.Database.Driver != "postgres" && c.Database.Driver != "mongo" {
		return errors.New("database.driver must be postgres or mongo")
	}
	if !namespaceName.MatchString(c.Database.Namespace) {
		return errors.New("database.namespace must be 1-48 lowercase letters, digits or underscores, beginning with a letter")
	}
	if c.Local {
		return errors.New("deployment configuration must set local=false; local-mode bypass is only for local runtime execution")
	}
	if c.Runtime != "dia" && c.Runtime != "framework" {
		return errors.New("runtime must be dia or framework")
	}
	if c.Runtime == "framework" {
		if !resourceName.MatchString(c.Service) || c.InternalSubject == "" || strings.ContainsAny(c.InternalSubject, " \t\r\n\x00") {
			return errors.New("framework requires service identifier and exact internalSubject")
		}
		if !namespaceName.MatchString(c.IdentityNamespace) || c.IdentityNamespace == c.Database.Namespace {
			return errors.New("framework requires a valid distinct identityNamespace")
		}
	} else if c.Service != "" || c.InternalSubject != "" || c.IdentityNamespace != "" {
		return errors.New("framework settings require runtime=framework")
	}
	if err := c.validatePeers(); err != nil {
		return err
	}
	if err := c.validateDelivery(); err != nil {
		return err
	}
	required := []string{"DIA_HMAC_KEY", "DIA_ENCRYPTION_KEY"}
	switch c.Signing.Provider {
	case "local":
		if c.Signing.KMSKey != "" {
			return errors.New("local signing forbids kmsKey")
		}
		if _, ok := c.Secrets["JWT_PUBLIC_KEY"]; ok {
			return errors.New("local signing forbids JWT_PUBLIC_KEY")
		}
		required = append(required, "JWT_PRIVATE_KEY")
	case "gcp", "aws":
		if c.Signing.KMSKey == "" || strings.ContainsAny(c.Signing.KMSKey, " \t\r\n\x00") {
			return errors.New("cloud signing requires kmsKey")
		}
		if _, ok := c.Secrets["JWT_PRIVATE_KEY"]; ok {
			return errors.New("cloud signing forbids JWT_PRIVATE_KEY")
		}
		required = append(required, "JWT_PUBLIC_KEY")
	default:
		return errors.New("signing.provider must be local, gcp or aws")
	}
	for _, key := range required {
		if _, ok := c.Secrets[key]; !ok {
			return fmt.Errorf("required secret reference %s is missing", key)
		}
	}
	for k, s := range c.Secrets {
		if !envName.MatchString(k) || reservedEnv(k) {
			return fmt.Errorf("invalid or reserved secret environment name %q", k)
		}
		if err := validateSecret(c.Provider, s); err != nil {
			return fmt.Errorf("secret %s: %w", k, err)
		}
	}
	if err := validateSecret(c.Provider, c.Database.Secret); err != nil {
		return fmt.Errorf("database.secret: %w", err)
	}
	switch c.Provider {
	case "gcp":
		if c.GCP == nil || c.AWS != nil {
			return errors.New("gcp provider requires only gcp configuration")
		}
		g := c.GCP
		if g.Invocation != "application" && g.Invocation != "iam" {
			return errors.New("gcp.invocation must explicitly select application or iam")
		}
		if c.Runtime == "framework" && g.Invocation == "iam" {
			return errors.New("framework GCP delivery requires invocation=application; intact Google token forwarding under Invoker IAM is unverified")
		}
		if !strings.HasPrefix(c.Publication.Topic, "projects/"+g.Project+"/topics/") || !strings.HasPrefix(c.Publication.Subscription, "projects/"+g.Project+"/subscriptions/") || (c.Publication.QueueURL != "" || c.Publication.QueueARN != "" || c.Publication.RawMessageDelivery || c.Publication.MaximumMessageSize != 0) {
			return errors.New("gcp requires publication topic and subscription in configured project, and no queueURL")
		}
		if !regexp.MustCompile(`^projects/[a-z0-9-]+/(topics|subscriptions)/[A-Za-z][A-Za-z0-9._~+%-]{2,254}$`).MatchString(c.Publication.Topic) || !regexp.MustCompile(`^projects/[a-z0-9-]+/subscriptions/[A-Za-z][A-Za-z0-9._~+%-]{2,254}$`).MatchString(c.Publication.Subscription) {
			return errors.New("invalid GCP publication resources")
		}
		if !resourceName.MatchString(g.Project) || !regionName.MatchString(g.Region) || !strings.HasSuffix(g.ServiceAccount, "@"+g.Project+".iam.gserviceaccount.com") || strings.ContainsAny(g.ServiceAccount, " \r\n") {
			return errors.New("gcp requires valid project, region and a service account in that project")
		}
	case "aws":
		if c.AWS == nil || c.GCP != nil {
			return errors.New("aws provider requires only aws configuration")
		}
		a := c.AWS
		queue, err := url.Parse(c.Publication.QueueURL)
		if !arn(c.Publication.Topic, "sns", ":") || err != nil || queue.Scheme != "https" || queue.User != nil || queue.Host != "sqs."+a.Region+".amazonaws.com" || len(strings.Split(strings.Trim(queue.Path, "/"), "/")) != 2 || queue.RawQuery != "" || queue.Fragment != "" || c.Publication.Subscription != "" {
			return errors.New("aws requires SNS topic ARN and regional HTTPS SQS queueURL, and no subscription")
		}
		queueParts := strings.Split(strings.Trim(queue.Path, "/"), "/")
		if !regexp.MustCompile(`^[0-9]{12}$`).MatchString(queueParts[0]) || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`).MatchString(queueParts[1]) || queue.Path != "/"+queueParts[0]+"/"+queueParts[1] || queue.RawPath != "" || c.Publication.QueueARN != "arn:aws:sqs:"+a.Region+":"+queueParts[0]+":"+queueParts[1] {
			return errors.New("publication queueARN must exactly match regional queueURL account and standard queue name")
		}
		if !c.Publication.RawMessageDelivery || c.Publication.MaximumMessageSize < 262144 || c.Publication.MaximumMessageSize > 1048576 {
			return errors.New("AWS publication requires rawMessageDelivery=true and maximumMessageSize between262144 and1048576 bytes")
		}
		if !regionName.MatchString(a.Region) || !resourceName.MatchString(a.Cluster) || !arn(a.ExecutionRole, "iam", ":role/") || !arn(a.TaskRole, "iam", ":role/") || !arn(a.TargetGroupARN, "elasticloadbalancing", ":targetgroup/") {
			return errors.New("aws requires region, cluster, execution/task role ARNs and an ALB target group ARN")
		}
		if len(a.Subnets) == 0 || len(a.SecurityGroups) == 0 || !strings.HasPrefix(a.LogGroup, "/") || strings.ContainsAny(a.LogGroup, " \r\n") {
			return errors.New("aws requires subnets, securityGroups and an absolute logGroup")
		}
		for _, id := range a.Subnets {
			if !regexp.MustCompile(`^subnet-[a-f0-9]+$`).MatchString(id) {
				return errors.New("invalid subnet ID")
			}
		}
		for _, id := range a.SecurityGroups {
			if !regexp.MustCompile(`^sg-[a-f0-9]+$`).MatchString(id) {
				return errors.New("invalid security group ID")
			}
		}
	default:
		return errors.New("provider must be gcp or aws")
	}
	return nil
}
func validImage(image string) bool {
	if !imageName.MatchString(image) || strings.Contains(image, "://") {
		return false
	}
	if strings.Contains(image, "@") {
		parts := strings.Split(image, "@sha256:")
		return len(parts) == 2 && parts[0] != "" && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(parts[1])
	}
	tail := image[strings.LastIndex(image, "/")+1:]
	parts := strings.Split(tail, ":")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "latest" && regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`).MatchString(parts[1])
}
func arn(s, service, resource string) bool {
	parts := strings.SplitN(s, ":", 6)
	return len(parts) == 6 && parts[0] == "arn" && (parts[1] == "aws" || parts[1] == "aws-us-gov" || parts[1] == "aws-cn") && parts[2] == service && regexp.MustCompile(`^[0-9]{12}$`).MatchString(parts[4]) && parts[5] != "" && strings.Contains(":"+parts[5], resource) && !strings.ContainsAny(s, " \r\n\x00")
}
func validateSecret(provider string, s Secret) error {
	switch provider {
	case "gcp":
		if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(s.Reference) || !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(s.Version) {
			return errors.New("GCP secret requires a Secret Manager ID and pinned numeric version")
		}
	case "aws":
		if !arn(s.Reference, "secretsmanager", ":secret/") && !arn(s.Reference, "secretsmanager", ":secret:") {
			return errors.New("AWS secret requires a Secrets Manager ARN")
		}
		if s.Version != "" {
			return errors.New("AWS version belongs in the secret ARN selector; omit version")
		}
	default:
		return errors.New("provider must be gcp or aws")
	}
	return nil
}
func reservedEnv(k string) bool {
	switch k {
	case "OUTBOUND_URL", "OUTBOUND_SERVICE", "DELEGATED_SUBJECT", "OUTBOUND_NETWORK", "OUTBOUND_SUBJECT", "DELIVERY_TASKS_QUEUE", "DELIVERY_URL", "DELIVERY_SERVICE_ACCOUNT", "DELIVERY_AUDIENCE", "DELIVERY_QUEUE_URL", "DELIVERY_GROUP_ID", "DELIVERY_TIMEOUT", "REPLAY_RUN_ID", "JWT_SIGNER", "JWT_KMS_KEY", "SERVICE", "INTERNAL_SUBJECT", "IDENTITY_DATABASE_NAMESPACE", "PUBLICATION_MODE", "PORT", "NETWORK", "LOCAL_MODE", "DATABASE_DRIVER", "DATABASE_NAMESPACE", "POSTGRES_URL", "MONGODB_URI", "MONGODB_DATABASE", "PUBLISHER", "PUBLICATION_TOPIC", "PUBLICATION_SUBSCRIPTION", "PUBLICATION_QUEUE_URL", "GCP_PROJECT", "AWS_REGION":
		return true
	}
	return false
}
func (c Config) DatabaseEnv() string {
	if c.Database.Driver == "mongo" {
		return "MONGODB_URI"
	}
	return "POSTGRES_URL"
}
func (c Config) RuntimeEnv() map[string]string {
	e := map[string]string{"NETWORK": c.Network, "LOCAL_MODE": "false", "DATABASE_DRIVER": c.Database.Driver, "DATABASE_NAMESPACE": c.Database.Namespace}
	e["JWT_SIGNER"] = c.Signing.Provider
	if c.Signing.KMSKey != "" {
		e["JWT_KMS_KEY"] = c.Signing.KMSKey
	}
	if c.Runtime == "framework" {
		delete(e, "LOCAL_MODE")
		e["PUBLICATION_MODE"] = c.Provider
		e["SERVICE"] = c.Service
		e["INTERNAL_SUBJECT"] = c.InternalSubject
		e["IDENTITY_DATABASE_NAMESPACE"] = c.IdentityNamespace
	} else {
		e["PUBLISHER"] = c.Provider
	}
	e["PUBLICATION_TOPIC"] = c.Publication.Topic
	if c.Provider == "gcp" {
		e["GCP_PROJECT"] = c.GCP.Project
		e["PUBLICATION_SUBSCRIPTION"] = c.Publication.Subscription
	} else {
		e["PUBLICATION_QUEUE_URL"] = c.Publication.QueueURL
		e["AWS_REGION"] = c.AWS.Region
	}
	if c.Database.Driver == "mongo" {
		e["MONGODB_DATABASE"] = c.Database.Namespace
	}
	c.deliveryEnv(e)
	if c.Peers != nil {
		for k, v := range map[string]string{"OUTBOUND_URL": c.Peers.OutboundURL, "OUTBOUND_SERVICE": c.Peers.OutboundService, "DELEGATED_SUBJECT": c.Peers.DelegatedSubject} {
			if v != "" {
				e[k] = v
			}
		}
	}
	return e
}

// Example returns illustrative identifiers only; no resource existence is asserted.
func Example(provider, driver string) Config {
	c := Config{Version: 1, Runtime: "dia", Signing: Signing{Provider: "local"}, Name: "dia-api", Network: "example.test", Provider: provider, Image: "example.invalid/blossm/dia:review-required", Port: 8080, Database: Database{Driver: driver, Namespace: "blossm"}}
	if provider == "gcp" {
		c.GCP = &GCP{Project: "example-project", Region: "us-central1", ServiceAccount: "dia-api@example-project.iam.gserviceaccount.com", Invocation: "application"}
		c.Database.Secret = Secret{Reference: "dia-database", Version: "1"}
		c.Publication = Publication{Topic: "projects/example-project/topics/dia-events", Subscription: "projects/example-project/subscriptions/dia-projection"}
		c.Secrets = map[string]Secret{"JWT_PRIVATE_KEY": {Reference: "dia-jwt-private-key", Version: "1"}, "DIA_HMAC_KEY": {Reference: "dia-hmac-key", Version: "1"}, "DIA_ENCRYPTION_KEY": {Reference: "dia-encryption-key", Version: "1"}}
	} else if provider == "aws" {
		c.AWS = &AWS{Region: "us-east-1", Cluster: "blossm-cluster", ExecutionRole: "arn:aws:iam::123456789012:role/dia-execution", TaskRole: "arn:aws:iam::123456789012:role/dia-runtime", Subnets: []string{"subnet-0123456789abcdef0"}, SecurityGroups: []string{"sg-0123456789abcdef0"}, LogGroup: "/ecs/dia-api", TargetGroupARN: "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/dia-api/0123456789abcdef"}
		c.Database.Secret = Secret{Reference: "arn:aws:secretsmanager:us-east-1:123456789012:secret:dia-database-AbCdEf"}
		c.Publication = Publication{Topic: "arn:aws:sns:us-east-1:123456789012:dia-events", QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/dia-projection", QueueARN: "arn:aws:sqs:us-east-1:123456789012:dia-projection", RawMessageDelivery: true, MaximumMessageSize: 262144}
		c.Secrets = map[string]Secret{"JWT_PRIVATE_KEY": {Reference: "arn:aws:secretsmanager:us-east-1:123456789012:secret:dia-jwt-private-key-AbCdEf"}, "DIA_HMAC_KEY": {Reference: "arn:aws:secretsmanager:us-east-1:123456789012:secret:dia-hmac-key-AbCdEf"}, "DIA_ENCRYPTION_KEY": {Reference: "arn:aws:secretsmanager:us-east-1:123456789012:secret:dia-encryption-key-AbCdEf"}}
	}
	return c
}
