package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationRuntimePlans(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		for _, driver := range []string{"postgres", "mongo"} {
			c := Example(provider, driver)
			c.Delivery = deliveryExample(provider)
			c.Env = map[string]string{"APP_SETTING": "configured"}
			p, err := Prepare(c)
			if err != nil {
				t.Fatal(err)
			}
			var artifact map[string]any
			file := "cloud-run.json"
			if provider == "aws" {
				file = "ecs-task.json"
			}
			if err = json.Unmarshal(p.Artifacts[file], &artifact); err != nil {
				t.Fatal(err)
			}
			var container map[string]any
			entry, envField := "command", "env"
			if provider == "gcp" {
				container = artifact["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
			} else {
				container = artifact["containerDefinitions"].([]any)[0].(map[string]any)
				entry = "entryPoint"
				envField = "environment"
			}
			if container[entry].([]any)[0] != "/server" {
				t.Fatal("wrong executable", container[entry])
			}
			env := map[string]string{}
			for _, value := range container[envField].([]any) {
				v := value.(map[string]any)
				if s, ok := v["value"].(string); ok {
					env[v["name"].(string)] = s
				}
			}
			for key, want := range map[string]string{"APP_SETTING": "configured", "DATABASE_NAMESPACE": "sample"} {
				if env[key] != want {
					t.Fatalf("%s=%q", key, env[key])
				}
			}
			for _, key := range []string{"LOCAL_MODE", "PUBLISHER", "PUBLICATION_MODE", "JWT_SIGNER"} {
				if _, ok := env[key]; ok {
					t.Fatalf("invented app requirement %s", key)
				}
			}
		}
	}
}

func TestSigningConfiguration(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		c := Example(provider, "postgres")
		c.Signing = &Signing{Provider: provider, KMSKey: "configured-key"}
		c.Secrets["JWT_PRIVATE_KEY"] = c.Secrets["API_KEY"]
		c.Secrets["JWT_PUBLIC_KEY"] = c.Secrets["JWT_PRIVATE_KEY"]
		if _, err := Prepare(c); err == nil {
			t.Fatal("mixed private and cloud signing accepted")
		}
		delete(c.Secrets, "JWT_PRIVATE_KEY")
		p, err := Prepare(c)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != "prepared-only" || c.RuntimeEnv()["JWT_KMS_KEY"] != "configured-key" || c.RuntimeEnv()["JWT_SIGNER"] != provider {
			t.Fatal("signer config lost")
		}
		delete(c.Secrets, "JWT_PUBLIC_KEY")
		if _, err = Prepare(c); err == nil {
			t.Fatal("missing pinned public key accepted")
		}
		c = Example(provider, "postgres")
		c.Signing = &Signing{Provider: "local", KMSKey: "unexpected"}
		if _, err = Prepare(c); err == nil {
			t.Fatal("local KMS key accepted")
		}
		c = Example(provider, "postgres")
		c.Signing = &Signing{}
		if _, err = Prepare(c); err == nil {
			t.Fatal("implicit signer accepted")
		}
	}
}

func TestDeliveryCheckedExamples(t *testing.T) {
	for _, name := range []string{"gcp-delivery-postgres.json", "aws-delivery-mongo.json"} {
		data, err := os.Open(filepath.Join("..", "deploy", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		c, err := DecodeConfig(data)
		data.Close()
		if err != nil {
			t.Fatal(err)
		}
		if c.Delivery == nil {
			t.Fatal("missing delivery")
		}
		if _, err = Prepare(c); err != nil {
			t.Fatal(err)
		}
	}
}

func deliveryExample(provider string) *Delivery {
	if provider == "gcp" {
		return &Delivery{TasksQueue: "projects/example-project/locations/us-central1/queues/document-delivery", URL: "https://framework.example.invalid/deliveries", ServiceAccount: "delivery@example-project.iam.gserviceaccount.com", Audience: "https://framework.example.invalid"}
	}
	return &Delivery{QueueURL: "https://sqs.us-east-1.amazonaws.com/123456789012/document-delivery", Timeout: "1m"}
}

func TestDeliveryConfigRejectsUnsafePlans(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		c := Example(provider, "postgres")
		if _, err := Prepare(c); err != nil {
			t.Fatal("optional delivery rejected", err)
		}
		c.Delivery = deliveryExample(provider)
		if _, err := Prepare(c); err != nil {
			t.Fatal(err)
		}
		if provider == "gcp" {
			if c.RuntimeEnv()["DELIVERY_AUDIENCE"] != c.Delivery.Audience {
				t.Fatal("audience lost")
			}
			c.Delivery.URL = "http://unsafe/deliveries"
		} else {
			if c.RuntimeEnv()["DELIVERY_QUEUE_URL"] != c.Delivery.QueueURL {
				t.Fatal("queue lost")
			}
			c.Delivery.QueueURL = c.Publication.QueueURL
		}
		if _, err := Prepare(c); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}

func TestPeerAuthorityConfiguration(t *testing.T) {
	c := Example("gcp", "postgres")
	c.Delivery = deliveryExample("gcp")
	c.Peers = &Peers{Network: "example.test", Service: "sample", OutboundURL: "https://child.example.invalid", OutboundService: "child", DelegatedSubject: "example.test/parent"}
	if _, err := Prepare(c); err != nil {
		t.Fatal(err)
	}
	env := c.RuntimeEnv()
	if env["OUTBOUND_SERVICE"] != "child" || env["OUTBOUND_URL"] != c.Peers.OutboundURL || env["DELEGATED_SUBJECT"] != "example.test/parent" {
		t.Fatal(env)
	}
	if env["OUTBOUND_NETWORK"] != "" || env["OUTBOUND_SUBJECT"] != "" {
		t.Fatal("invented identity override")
	}
	for _, bad := range []Peers{{OutboundService: "child"}, {OutboundURL: "https://child.example.invalid"}, {OutboundURL: "http://child.example.invalid", OutboundService: "child"}, {DelegatedSubject: "foreign/parent"}, {DelegatedSubject: "example.test/parent/nested"}, {OutboundURL: "https://user:secret@child.example.invalid", OutboundService: "child"}} {
		bad.Network = "example.test"
		bad.Service = "sample"
		c.Peers = &bad
		if _, err := Prepare(c); err == nil {
			t.Fatal("unsafe peer config accepted", bad)
		}
	}
}

func TestCloudRunExplicitInvocationMode(t *testing.T) {
	for _, mode := range []string{"application", "iam", "", "implicit"} {
		c := Example("gcp", "postgres")
		c.GCP.Invocation = mode
		p, err := Prepare(c)
		if mode == "" || mode == "implicit" {
			if err == nil {
				t.Fatal("accepted implicit invocation policy")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		var service struct {
			Metadata struct{ Annotations map[string]string }
		}
		if err = json.Unmarshal(p.Artifacts["cloud-run.json"], &service); err != nil {
			t.Fatal(err)
		}
		want := "false"
		if mode == "application" {
			want = "true"
		}
		if service.Metadata.Annotations["run.googleapis.com/invoker-iam-disabled"] != want || service.Metadata.Annotations["run.googleapis.com/ingress"] != "internal-and-cloud-load-balancing" {
			t.Fatal(service)
		}
		review := false
		for _, note := range p.Prerequisites {
			if strings.Contains(note, "REVIEW: selected "+mode+" invocation mode") {
				review = true
			}
		}
		if !review || p.Status != "prepared-only" {
			t.Fatal("missing invocation review", p)
		}
	}
}

func TestPlansRequireOperatorRedrivePolicy(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		p, err := Prepare(Example(provider, "postgres"))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, note := range p.Prerequisites {
			if strings.Contains(note, "does not provision a DLQ or redrive policy") && strings.Contains(note, "FIFO blocking") {
				found = true
			}
		}
		if !found {
			t.Fatal("missing operator recovery prerequisite", provider)
		}
	}
}

func TestRejectsUnverifiedCloudRunIAMDelivery(t *testing.T) {
	c := Example("gcp", "postgres")
	c.Delivery = deliveryExample("gcp")
	c.GCP.Invocation = "iam"
	if _, err := Prepare(c); err == nil || !strings.Contains(err.Error(), "intact Google token forwarding") {
		t.Fatal("unsupported delivery mode accepted", err)
	}
	c.GCP.Invocation = "application"
	if _, err := Prepare(c); err != nil {
		t.Fatal(err)
	}
	c = Example("gcp", "postgres")
	c.GCP.Invocation = "iam"
	if _, err := Prepare(c); err != nil {
		t.Fatal("explicit IAM edge mode rejected", err)
	}
}

// Signing credentials have one source: secret references, never inline environment.
func TestSigningCredentialEnvironmentRejected(t *testing.T) {
	for _, platform := range []string{"gcp", "aws"} {
		for _, signer := range []string{"local", "gcp", "aws"} {
			t.Run(platform+"/"+signer, func(t *testing.T) {
				c := Example(platform, "postgres")
				c.Signing = &Signing{Provider: signer}
				required := "JWT_PRIVATE_KEY"
				if signer != "local" {
					c.Signing.KMSKey = "configured-key"
					required = "JWT_PUBLIC_KEY"
				}
				c.Secrets[required] = c.Secrets["API_KEY"]
				p, err := Prepare(c)
				if err != nil || p.Status != "prepared-only" {
					t.Fatal("valid signer reference rejected", err)
				}
				if _, ok := c.RuntimeEnv()[required]; ok {
					t.Fatal("secret rendered as plain env")
				}
				for _, key := range []string{"JWT_PRIVATE_KEY", "JWT_PUBLIC_KEY"} {
					for _, value := range []string{"", "sensitive-inline-key"} {
						c.Env = map[string]string{key: value}
						p, err = Prepare(c)
						if err == nil || !strings.Contains(err.Error(), "secret reference") {
							t.Fatalf("accepted inline %s: %v", key, err)
						}
						if len(p.Artifacts) != 0 || strings.Contains(err.Error(), "sensitive-inline-key") {
							t.Fatal("rejected key leaked into output")
						}
					}
				}
				c.Env = nil
				delete(c.Secrets, required)
				if _, err = Prepare(c); err == nil {
					t.Fatal("missing required key reference accepted")
				}
			})
		}
	}
	for _, key := range []string{"JWT_PRIVATE_KEY", "JWT_PUBLIC_KEY"} {
		c := Example("gcp", "postgres")
		c.Env = map[string]string{key: "sensitive-inline-key"}
		if _, err := Prepare(c); err == nil {
			t.Fatal("omitting signer bypassed reference requirement", key)
		}
	}
}

func TestMongoPhysicalDatabaseIndependentOfNamespace(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		for _, namespace := range []string{"identity", "documents"} {
			c := Example(provider, "mongo")
			c.Database.Name = "shared_app"
			c.Database.Namespace = namespace
			p, err := Prepare(c)
			if err != nil {
				t.Fatal(err)
			}
			if c.RuntimeEnv()["MONGODB_DATABASE"] != "shared_app" || c.RuntimeEnv()["DATABASE_NAMESPACE"] != namespace {
				t.Fatal("physical database coupled to namespace", c.RuntimeEnv())
			}
			artifact := "cloud-run.json"
			if provider == "aws" {
				artifact = "ecs-task.json"
			}
			var root map[string]any
			if err = json.Unmarshal(p.Artifacts[artifact], &root); err != nil {
				t.Fatal(err)
			}
			var container map[string]any
			field := "env"
			if provider == "gcp" {
				container = root["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
			} else {
				container = root["containerDefinitions"].([]any)[0].(map[string]any)
				field = "environment"
			}
			env := map[string]string{}
			for _, item := range container[field].([]any) {
				entry := item.(map[string]any)
				if value, ok := entry["value"].(string); ok {
					env[entry["name"].(string)] = value
				}
			}
			if env["MONGODB_DATABASE"] != "shared_app" || env["DATABASE_NAMESPACE"] != namespace {
				t.Fatal("incorrect prepared database environment", env)
			}
		}
		for _, name := range []string{"", "../db", "db.with.dot", "db/name", "db name", "db\x00name", strings.Repeat("a", 64)} {
			c := Example(provider, "mongo")
			c.Database.Name = name
			if _, err := Prepare(c); err == nil {
				t.Fatal("unsafe mongo physical name", name)
			}
		}
		for _, name := range []string{"1DB-name_", strings.Repeat("a", 63)} {
			c := Example(provider, "mongo")
			c.Database.Name = name
			if _, err := Prepare(c); err != nil {
				t.Fatal("valid backend physical name rejected", name, err)
			}
			if c.RuntimeEnv()["MONGODB_DATABASE"] != name {
				t.Fatal("physical name normalized")
			}
		}
		c := Example(provider, "postgres")
		if _, err := Prepare(c); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.RuntimeEnv()["MONGODB_DATABASE"]; ok {
			t.Fatal("postgres emitted mongo database")
		}
		c.Database.Name = "unexpected"
		if _, err := Prepare(c); err == nil {
			t.Fatal("postgres accepted mongo-only name")
		}
	}
}
