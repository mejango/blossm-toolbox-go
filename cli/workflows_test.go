package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func configFile(t *testing.T, provider string) string {
	t.Helper()
	name := filepath.Join(t.TempDir(), "config.json")
	b, _ := encode(Example(provider, "postgres"))
	if err := os.WriteFile(name, b, 0600); err != nil {
		t.Fatal(err)
	}
	return name
}
func TestResourceWorkflowsPrepareAndExecute(t *testing.T) {
	roles := filepath.Join(t.TempDir(), "roles ; literal.json")
	if err := os.WriteFile(roles, []byte(`{"reader":["read"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{"gcp", "aws"} {
		t.Run(provider, func(t *testing.T) {
			config := configFile(t, provider)
			bucket := "gs://example-bucket/roles.json"
			queueURL := "queue-name"
			if provider == "aws" {
				bucket = "s3://example-bucket/roles.json"
				queueURL = "https://sqs.us-east-1.amazonaws.com/123456789012/queue-name"
			}
			cases := [][]string{{"roles", "--action", "upload", "--input", roles, "--destination", bucket}, {"secret", "--action", "create", "--name", "new-secret", "--value-env", "TEST_SECRET"}, {"secret", "--action", "put", "--name", "existing-secret", "--value-env", "TEST_SECRET"}, {"queue", "--action", "create", "--name", "queue-name"}, {"queue", "--action", "update", "--name", queueURL}, {"queue", "--action", "delete", "--name", queueURL}}
			for _, args := range cases {
				t.Run(strings.Join(args[:3], "/"), func(t *testing.T) {
					var calls []Invocation
					var out bytes.Buffer
					e := Execution{Runner: func(_ context.Context, in Invocation) ([]byte, error) {
						calls = append(calls, in)
						return []byte(`{}`), nil
					}, Getenv: func(name string) string {
						if name != "TEST_SECRET" {
							t.Fatal(name)
						}
						return "$(do-not-execute)\nprivate-bytes"
					}}
					base := append(append([]string{}, args...), "--config", config)
					if err := RunWith(context.Background(), base, &out, &out, e); err != nil {
						t.Fatal(err)
					}
					if len(calls) != 0 {
						t.Fatal("prepared workflow executed")
					}
					if strings.Contains(out.String(), "private-bytes") {
						t.Fatal("plan leaked secret")
					}
					var p WorkflowPlan
					if err := json.Unmarshal(out.Bytes(), &p); err != nil {
						t.Fatal(err)
					}
					out.Reset()
					if err := RunWith(context.Background(), append(base, "--execute"), &out, &out, e); err != nil {
						t.Fatal(err)
					}
					if len(calls) != len(p.Actions) {
						t.Fatal("execution differs from plan")
					}
					for i, a := range p.Actions {
						if !reflect.DeepEqual(a.Args, calls[i].Args) {
							t.Fatal("argv drift")
						}
						if a.InputEnv != "" && string(calls[i].Input) != "$(do-not-execute)\nprivate-bytes" {
							t.Fatal("secret stdin changed")
						}
						for _, arg := range calls[i].Args {
							if strings.Contains(arg, "private-bytes") {
								t.Fatal("credential in argv")
							}
						}
					}
					if args[0] == "roles" {
						found := false
						for _, arg := range calls[0].Args {
							if arg == roles {
								found = true
							}
						}
						if !found {
							t.Fatal("literal filename lost")
						}
					}
				})
			}
		})
	}
}
func TestProviderFailureStopsExecution(t *testing.T) {
	expected := errors.New("fixture denied")
	calls := 0
	var out bytes.Buffer
	e := Execution{Runner: func(context.Context, Invocation) ([]byte, error) { calls++; return nil, expected }, Getenv: func(string) string { return "secret" }}
	err := RunWith(context.Background(), []string{"secret", "--config", configFile(t, "gcp"), "--action", "create", "--name", "secret-name", "--value-env", "VALUE", "--execute"}, &out, &out, e)
	if !errors.Is(err, expected) || calls != 1 {
		t.Fatalf("failure lost or execution continued: %v calls=%d", err, calls)
	}
	calls = 0
	e.Getenv = func(string) string { return "" }
	err = RunWith(context.Background(), []string{"secret", "--config", configFile(t, "gcp"), "--action", "create", "--name", "secret-name", "--value-env", "VALUE", "--execute"}, &out, &out, e)
	if err == nil || calls != 0 {
		t.Fatal("mutated before validating secret prerequisite")
	}
}
func TestDeployExecutionUsesRegisteredRevision(t *testing.T) {
	for _, update := range []bool{false, true} {
		t.Run(map[bool]string{false: "create", true: "update"}[update], func(t *testing.T) {
			arn := "arn:aws:ecs:us-east-1:123456789012:task-definition/dia-api:42"
			var calls []Invocation
			var out bytes.Buffer
			e := Execution{Runner: func(_ context.Context, in Invocation) ([]byte, error) {
				calls = append(calls, in)
				switch len(calls) {
				case 1:
					if in.Args[2] != "register-task-definition" {
						t.Fatal(in.Args)
					}
					if _, err := os.ReadFile(filepath.Join(in.Dir, "ecs-task.json")); err != nil {
						t.Fatal(err)
					}
					return []byte(`{"taskDefinition":{"taskDefinitionArn":"` + arn + `"}}`), nil
				case 2:
					if update {
						if in.Args[2] != "update-service" || !strings.Contains(strings.Join(in.Args, " "), arn) {
							t.Fatal(in.Args)
						}
					} else {
						if in.Args[2] != "create-service" {
							t.Fatal(in.Args)
						}
						data, err := os.ReadFile(filepath.Join(in.Dir, "ecs-service.json"))
						if err != nil || !bytes.Contains(data, []byte(arn)) {
							t.Fatal("did not bind exact revision", err)
						}
					}
				case 3:
					if in.Args[2] != "wait" || in.Args[3] != "services-stable" {
						t.Fatal(in.Args)
					}
				default:
					t.Fatal("unexpected call")
				}
				return []byte(`{}`), nil
			}}
			args := []string{"deploy", "--config", configFile(t, "aws"), "--execute"}
			if update {
				args = append(args, "--update")
			}
			if err := RunWith(context.Background(), args, &out, &out, e); err != nil {
				t.Fatal(err)
			}
			if len(calls) != 3 {
				t.Fatal(calls)
			}
			if _, err := os.Stat(calls[0].Dir); !os.IsNotExist(err) {
				t.Fatal("temporary deploy files retained")
			}
		})
	}
}
func TestDeployMalformedReceiptAndFailure(t *testing.T) {
	var out bytes.Buffer
	for _, provider := range []string{"gcp", "aws"} {
		calls := 0
		expected := errors.New("fixture failure")
		e := Execution{Runner: func(context.Context, Invocation) ([]byte, error) { calls++; return nil, expected }}
		if err := RunWith(context.Background(), []string{"deploy", "--config", configFile(t, provider), "--execute"}, &out, &out, e); !errors.Is(err, expected) || calls != 1 {
			t.Fatal(err, calls)
		}
	}
	calls := 0
	e := Execution{Runner: func(context.Context, Invocation) ([]byte, error) { calls++; return []byte(`{}`), nil }}
	if err := RunWith(context.Background(), []string{"deploy", "--config", configFile(t, "aws"), "--execute"}, &out, &out, e); err == nil || calls != 1 {
		t.Fatal("invalid task receipt was accepted")
	}
}

type workflowTransport func(*http.Request) (*http.Response, error)

func (f workflowTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestAuthenticatedJobReplay(t *testing.T) {
	for _, name := range []string{"job", "replay"} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			status := 200
			var out bytes.Buffer
			e := Execution{Getenv: func(name string) string {
				if name != "TOKEN_ENV" {
					t.Fatal(name)
				}
				return "private-token"
			}, HTTP: &http.Client{Transport: workflowTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "Bearer private-token" || r.Header.Get("Idempotency-Key") != "stable" || r.URL.Path != map[string]string{"job": "/operations/job", "replay": "/projections/documents/replay"}[name] || r.Method != "POST" {
					t.Error("bad authenticated invocation")
				}
				body := `{"accepted":true}`
				if name == "replay" {
					body = `{"runId":"rebuild-1","enqueued":0,"after":"","complete":true}`
					input, _ := io.ReadAll(r.Body)
					if !bytes.Contains(input, []byte(`"runId":"rebuild-1"`)) {
						t.Error("lost run ID")
					}
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			args := []string{name, "--config", configFile(t, "gcp"), "--url", "https://operation.example/operations/" + name, "--bearer-env", "TOKEN_ENV", "--idempotency-key", "stable"}
			if name == "replay" {
				args[4] = "https://operation.example/projections/documents/replay"
				input := filepath.Join(t.TempDir(), "replay.json")
				if err := os.WriteFile(input, []byte(`{"runId":"rebuild-1","limit":10}`), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--input", input)
			}
			if err := RunWith(context.Background(), args, &out, &out, e); err != nil || calls != 0 {
				t.Fatal(err, calls)
			}
			if strings.Contains(out.String(), "private-token") {
				t.Fatal("token leaked")
			}
			out.Reset()
			if err := RunWith(context.Background(), append(args, "--execute"), &out, &out, e); err != nil || calls != 1 {
				t.Fatal(err, calls)
			}
			status = 403
			if err := RunWith(context.Background(), append(args, "--execute"), &out, &out, e); err == nil {
				t.Fatal("HTTP failure swallowed")
			}
		})
	}
}
func TestScaffoldsCompileInOneModule(t *testing.T) {
	original, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	mod := fmtModule(original)
	if err = os.WriteFile(filepath.Join(root, "go.mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for kind := range procedureTemplates {
		name := strings.ReplaceAll(kind, "-", "_")
		args := []string{"scaffold", "--dir", root, "--kind", kind, "--name", name}
		if err = Run(context.Background(), args, &out, &out); err != nil {
			t.Fatal(err)
		}
		if err = Run(context.Background(), args, &out, &out); err == nil {
			t.Fatal("overwrote scaffold")
		}
		if _, err = os.Stat(filepath.Join(root, name, "go.mod")); !os.IsNotExist(err) {
			t.Fatal("created nested module")
		}
	}
	const ownerFixture = `package fixture_test
import (
 "context"
 "testing"
 "github.com/mejango/blossm-toolbox-go/procedures/job"
 "github.com/mejango/blossm-toolbox-go/procedures/projection"
 "github.com/mejango/blossm-toolbox-go/stores/view"
 "github.com/mejango/blossm-toolbox-go/transport"
 g "fixture.test/gateway"
 j "fixture.test/job"
 p "fixture.test/projection"
 v "fixture.test/view_composite"
)
func TestGeneratedOwnerContracts(t *testing.T){
 if app,err:=g.New(context.Background(),transport.Framework{});err==nil || app!=nil{t.Fatal("gateway bypassed framework configuration validation")}
 var procedure job.Procedure=j.New(nil)
 if err:=procedure.Execute(context.Background(),job.Input{Payload:[]byte("{}")});err==nil{t.Fatal("missing job handler accepted")}
 var engine *projection.Engine=p.New(nil,nil,nil)
 if _,err:=engine.Play(context.Background(),projection.Request{});err==nil{t.Fatal("missing projection dependencies accepted")}
 var scheduler projection.Scheduler=p.ReplayScheduler(nil,nil)
 if _,err:=scheduler.Replay(context.Background(),projection.Source{},projection.RunID("fixture-run"),"",1);err==nil{t.Fatal("missing replay dependencies accepted")}
 if _,err:=v.Execute[string,string](context.Background(),view.Scope{},"",nil,nil);err==nil{t.Fatal("composite bypassed owner validation")}
}
`
	if err = os.WriteFile(filepath.Join(root, "owners_test.go"), []byte(ownerFixture), 0600); err != nil {
		t.Fatal(err)
	}
	if err = runGo(context.Background(), root, []string{"test", "-mod=mod", "./..."}, &out, &out); err != nil {
		t.Fatal(err, out.String())
	}
}
func fmtModule(original string) string {
	return "module fixture.test\n\ngo 1.26.0\n\nrequire github.com/mejango/blossm-toolbox-go v0.0.0\nreplace github.com/mejango/blossm-toolbox-go => " + strconvQuote(original) + "\n"
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestDefaultDeployNeverRunsOrReadsSecrets(t *testing.T) {
	var out bytes.Buffer
	e := Execution{Runner: func(context.Context, Invocation) ([]byte, error) { t.Fatal("default deploy executed"); return nil, nil }, Getenv: func(string) string { t.Fatal("default deploy read secret"); return "" }}
	for _, provider := range []string{"gcp", "aws"} {
		if err := RunWith(context.Background(), []string{"deploy", "--config", configFile(t, provider)}, &out, &out, e); err != nil {
			t.Fatal(err)
		}
	}
}
