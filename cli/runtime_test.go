package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFrameworkRuntimePlans(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		for _, driver := range []string{"postgres", "mongo"} {
			c := Example(provider, driver)
			c.Delivery = deliveryExample(provider)
			c.Runtime = "framework"
			c.Service = "documents"
			c.InternalSubject = "document-peer"
			c.IdentityNamespace = "blossm_identity"
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
			if container[entry].([]any)[0] != "/framework" {
				t.Fatal("wrong executable", container[entry])
			}
			env := map[string]string{}
			for _, value := range container[envField].([]any) {
				v := value.(map[string]any)
				if s, ok := v["value"].(string); ok {
					env[v["name"].(string)] = s
				}
			}
			for key, want := range map[string]string{"SERVICE": "documents", "INTERNAL_SUBJECT": "document-peer", "IDENTITY_DATABASE_NAMESPACE": "blossm_identity", "DATABASE_NAMESPACE": "blossm", "PUBLICATION_MODE": provider, "JWT_SIGNER": "local"} {
				if env[key] != want {
					t.Fatalf("%s=%q, want %q", key, env[key], want)
				}
			}
			if _, ok := env["LOCAL_MODE"]; ok {
				t.Fatal("framework received Dia local contract")
			}
			if _, ok := env["PUBLISHER"]; ok {
				t.Fatal("framework received Dia publisher contract")
			}
			c.IdentityNamespace = c.Database.Namespace
			if _, err = Prepare(c); err == nil {
				t.Fatal("shared namespace accepted")
			}
		}
	}
}

func TestSigningConfiguration(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		c := Example(provider, "postgres")
		c.Signing = Signing{Provider: provider, KMSKey: "configured-key"}
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
		c.Signing.KMSKey = "unexpected"
		if _, err = Prepare(c); err == nil {
			t.Fatal("local KMS key accepted")
		}
		c = Example(provider, "postgres")
		c.Signing.Provider = ""
		if _, err = Prepare(c); err == nil {
			t.Fatal("implicit signer accepted")
		}
	}
}

func TestFrameworkCheckedExamples(t *testing.T) {
	for _, name := range []string{"gcp-framework-postgres.json", "aws-framework-mongo.json"} {
		data, err := os.Open(filepath.Join("..", "deploy", "examples", name))
		if err != nil {
			t.Fatal(err)
		}
		c, err := DecodeConfig(data)
		data.Close()
		if err != nil {
			t.Fatal(err)
		}
		if c.Runtime != "framework" {
			t.Fatal("wrong runtime")
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
		c.Runtime = "framework"
		c.Service = "documents"
		c.InternalSubject = "peer"
		c.IdentityNamespace = "identity"
		if _, err := Prepare(c); err == nil {
			t.Fatal("missing delivery accepted")
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

func TestFrameworkPeerAuthorityConfiguration(t *testing.T) {
	c := Example("gcp", "postgres")
	c.Runtime = "framework"
	c.Service = "documents"
	c.InternalSubject = "peer"
	c.IdentityNamespace = "identity"
	c.Delivery = deliveryExample("gcp")
	c.Peers = &Peers{OutboundURL: "https://child.example.invalid", OutboundService: "child", DelegatedSubject: "example.test/parent"}
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

func TestFrameworkRejectsUnverifiedCloudRunIAMDelivery(t *testing.T) {
	c := internalExample()
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
		t.Fatal("explicit Dia edge mode rejected", err)
	}
}
