package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/mejango/blossm-toolbox-go/stores/event"
	"reflect"
	"testing"
)

func TestAWSPublicationSizing(t *testing.T) {
	if eventstore.MaxPublicationBytes+77 > 262144 {
		t.Fatal("publication plus eventId attribute exceeds SNS limit; review delivery contract")
	}
	c := Example("aws", "mongo")
	p, err := Prepare(c)
	if err != nil {
		t.Fatal(err)
	}
	var attributes map[string]string
	if err = json.Unmarshal(p.Artifacts["sqs-publication-attributes.json"], &attributes); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(attributes, map[string]string{"MaximumMessageSize": "262144"}) {
		t.Fatal(attributes)
	}
	var sub struct {
		TopicArn, Protocol, Endpoint string
		Attributes                   map[string]string
		ReturnSubscriptionArn        bool
	}
	if err = json.Unmarshal(p.Artifacts["sns-publication-subscription.json"], &sub); err != nil {
		t.Fatal(err)
	}
	if sub.TopicArn != c.Publication.Topic || sub.Protocol != "sqs" || sub.Endpoint != c.Publication.QueueARN || !sub.ReturnSubscriptionArn || !reflect.DeepEqual(sub.Attributes, map[string]string{"RawMessageDelivery": "true"}) {
		t.Fatalf("subscription %#v", sub)
	}
	want := [][]string{
		{"aws", "sqs", "set-queue-attributes", "--queue-url", c.Publication.QueueURL, "--attributes", "file://sqs-publication-attributes.json", "--region", c.AWS.Region, "--no-cli-pager"},
		{"aws", "sns", "subscribe", "--cli-input-json", "file://sns-publication-subscription.json", "--region", c.AWS.Region, "--no-cli-pager"},
	}
	if !reflect.DeepEqual(p.PreparationCommands, want) {
		t.Fatal(p.PreparationCommands)
	}
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Publication.RawMessageDelivery = false },
		func(c *Config) { c.Publication.MaximumMessageSize = 262143 },
		func(c *Config) { c.Publication.MaximumMessageSize = 1048577 },
		func(c *Config) { c.Publication.QueueARN = "" },
		func(c *Config) { c.Publication.QueueARN = "arn:aws:sqs:us-east-1:000000000000:dia-projection" },
		func(c *Config) { c.Publication.QueueURL += "/" },
	} {
		bad := Example("aws", "mongo")
		mutate(&bad)
		if _, err := Prepare(bad); err == nil {
			t.Fatalf("accepted invalid delivery config %#v", bad.Publication)
		}
	}
	for _, size := range []int{262144, 1048576} {
		larger := Example("aws", "postgres")
		larger.Publication.MaximumMessageSize = size
		if _, err := Prepare(larger); err != nil {
			t.Fatal(err)
		}
	}
	gcp := Example("gcp", "postgres")
	gcp.Publication.RawMessageDelivery = true
	if err := gcp.Validate(); err == nil {
		t.Fatal("GCP accepted AWS settings")
	}
	// Preparation must not execute provider operations or discover credentials.
	var out bytes.Buffer
	if err := RunWith(context.Background(), []string{"deploy", "--config", configFile(t, "aws")}, &out, &out, Execution{Runner: func(context.Context, Invocation) ([]byte, error) { t.Fatal("provider executed"); return nil, nil }, Getenv: func(string) string { t.Fatal("credentials read"); return "" }}); err != nil {
		t.Fatal(err)
	}
}
