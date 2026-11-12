package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func fixtureEnv(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{"NETWORK": "fixture", "SERVICE": "notes", "INTERNAL_SUBJECT": "worker", "PUBLICATION_MODE": "inline", "DATABASE_DRIVER": "postgres", "DATABASE_NAMESPACE": "notes", "IDENTITY_DATABASE_NAMESPACE": "identity", "POSTGRES_URL": "postgres://fixture", "DIA_HMAC_KEY": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("h", 32))), "DIA_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte(strings.Repeat("e", 32))), "PORT": "", "CORS_ORIGINS": "", "OUTBOUND_URL": "", "OUTBOUND_SERVICE": "", "DELEGATED_SUBJECT": ""} {
		t.Setenv(name, value)
	}
}
func TestConfigurationModes(t *testing.T) {
	for _, mode := range []string{"local", "inline", "gcp", "aws"} {
		t.Run(mode, func(t *testing.T) {
			fixtureEnv(t)
			t.Setenv("PUBLICATION_MODE", mode)
			c, err := loadConfig()
			if err != nil || c.Mode != mode || c.Port != "8080" || c.IdentityNamespace == c.DocumentNamespace {
				t.Fatal(c, err)
			}
		})
	}
}
func TestConfigurationFailsClosed(t *testing.T) {
	for _, change := range []struct{ name, value string }{{"NETWORK", ""}, {"SERVICE", ""}, {"INTERNAL_SUBJECT", ""}, {"PUBLICATION_MODE", ""}, {"PUBLICATION_MODE", "auto"}, {"IDENTITY_DATABASE_NAMESPACE", "notes"}, {"IDENTITY_DATABASE_NAMESPACE", ""}, {"DIA_HMAC_KEY", "bad"}, {"DIA_ENCRYPTION_KEY", ""}, {"PORT", "0"}, {"PORT", "65536"}, {"CORS_ORIGINS", "{}"}, {"DATABASE_DRIVER", "other"}} {
		t.Run(change.name+change.value, func(t *testing.T) {
			fixtureEnv(t)
			t.Setenv(change.name, change.value)
			if _, err := loadConfig(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestPublicationAndDeliveryQueuesMustDiffer(t *testing.T) {
	fixtureEnv(t)
	t.Setenv("PUBLICATION_MODE", "aws")
	t.Setenv("PUBLICATION_QUEUE_URL", "https://sqs.test/publications")
	t.Setenv("DELIVERY_QUEUE_URL", "https://sqs.test/publications")
	if _, err := loadConfig(); err == nil {
		t.Fatal("competing wire protocols shared one queue")
	}
	t.Setenv("DELIVERY_QUEUE_URL", "https://sqs.test/deliveries")
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
}
