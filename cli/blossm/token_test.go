package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDeveloperToken(t *testing.T) {
	config := configFile(t, "gcp")
	base := []string{"token", "--config", config, "--audience", "https://service.example", "--impersonate-service-account", "caller@example.iam.gserviceaccount.com"}
	var out bytes.Buffer
	calls := 0
	failure := errors.New("provider failed")
	var resultErr error
	result := []byte("fixture.identity.token\n")
	e := Execution{Runner: func(_ context.Context, in Invocation) ([]byte, error) {
		calls++
		want := []string{"gcloud", "auth", "print-identity-token", "--audiences=https://service.example", "--project=" + Example("gcp", "postgres").GCP.Project, "--quiet", "--impersonate-service-account=caller@example.iam.gserviceaccount.com"}
		if !reflect.DeepEqual(in.Args, want) || len(in.Input) != 0 {
			t.Fatalf("unexpected invocation: %#v", in)
		}
		return result, resultErr
	}}
	if err := RunWith(context.Background(), base, &out, &out, e); err != nil || calls != 0 || !strings.Contains(out.String(), "prepared-only") {
		t.Fatalf("offline: %v %d %s", err, calls, &out)
	}
	out.Reset()
	if err := RunWith(context.Background(), append(base, "--execute"), &out, &out, e); err != nil || calls != 1 || out.String() != "fixture.identity.token\n" {
		t.Fatalf("execute: %v %d %s", err, calls, &out)
	}
	resultErr = failure
	out.Reset()
	if err := RunWith(context.Background(), append(base, "--execute"), &out, &out, e); !errors.Is(err, failure) || out.Len() != 0 {
		t.Fatalf("failure: %v %s", err, &out)
	}
	resultErr = nil
	for _, bad := range []string{"", "first\nsecond", "invalid token"} {
		result = []byte(bad)
		if err := RunWith(context.Background(), append(base, "--execute"), &out, &out, e); err == nil || out.Len() != 0 {
			t.Fatal("accepted invalid output")
		}
	}
	previous := calls
	for _, args := range [][]string{
		append(append([]string{}, base...), "--execute", "--dry-run"),
		{"token", "--config", config, "--audience", "a,b", "--execute"},
		{"token", "--config", config, "--execute"},
		{"token", "--config", configFile(t, "aws"), "--execute"},
	} {
		if err := RunWith(context.Background(), args, &out, &out, e); err == nil {
			t.Fatal("accepted invalid request")
		}
		if calls != previous {
			t.Fatal("invalid request invoked provider")
		}
	}
}
