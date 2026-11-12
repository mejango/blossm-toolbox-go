package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStrictConfiguration(t *testing.T) {
	c := Example("gcp", "postgres")
	data, _ := encode(c)
	if _, err := DecodeConfig(bytes.NewReader(data)); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"unknown":          strings.Replace(string(data), `"version": 2`, `"version": 2, "typo": true`, 1),
		"duplicate":        strings.Replace(string(data), `"version": 2`, `"version": 2, "version": 2`, 1),
		"nested duplicate": strings.Replace(string(data), `"driver": "postgres"`, `"driver": "postgres", "driver": "mongo"`, 1),
		"trailing":         string(data) + ` {}`, "null": "null", "missing": `{}`, "oversized": strings.Repeat(" ", 1<<20+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeConfig(strings.NewReader(data)); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
	for name, mutate := range map[string]func(*Config){
		"driver": func(c *Config) { c.Database.Driver = "memory" }, "package": func(c *Config) { c.Application.Package = "../escape" },
		"image": func(c *Config) { c.Image = "repo:latest" }, "cross-provider": func(c *Config) { c.AWS = &AWS{} },
		"secret": func(c *Config) { c.Database.Secret.Version = "latest" }, "reserved": func(c *Config) { c.Secrets = map[string]Secret{"PORT": {Reference: "port", Version: "1"}} },
	} {
		t.Run(name, func(t *testing.T) {
			copy := Example("gcp", "postgres")
			mutate(&copy)
			if err := copy.Validate(); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}
func TestArtifacts(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		for _, driver := range []string{"postgres", "mongo"} {
			t.Run(provider+"/"+driver, func(t *testing.T) {
				c := Example(provider, driver)
				p, err := Prepare(c)
				if err != nil {
					t.Fatal(err)
				}
				p2, err := Prepare(c)
				if err != nil {
					t.Fatal(err)
				}
				a, _ := encode(p)
				b, _ := encode(p2)
				if !bytes.Equal(a, b) {
					t.Fatal("nondeterministic output")
				}
				if p.Status != "prepared-only" {
					t.Fatal("unexpected deployment claim")
				}
				if !bytes.Contains(a, []byte(c.DatabaseEnv())) {
					t.Fatal("database env missing")
				}
				if provider == "gcp" {
					var service struct {
						Spec struct {
							Template struct {
								Spec struct {
									Containers []struct {
										Env []struct {
											Name string `json:"name"`
										}
									}
								}
							}
						}
					}
					if err = json.Unmarshal(p.Artifacts["cloud-run.json"], &service); err != nil {
						t.Fatal(err)
					}
					for _, e := range service.Spec.Template.Spec.Containers[0].Env {
						if e.Name == "PORT" {
							t.Fatal("Cloud Run owns PORT")
						}
					}
				} else {
					var task map[string]any
					if err = json.Unmarshal(p.Artifacts["ecs-task.json"], &task); err != nil {
						t.Fatal(err)
					}
					if task["networkMode"] != "awsvpc" || task["cpu"] != "256" || task["memory"] != "512" {
						t.Fatal("invalid Fargate resources")
					}
					var service map[string]any
					if err = json.Unmarshal(p.Artifacts["ecs-service.json"], &service); err != nil {
						t.Fatal(err)
					}
					if service["loadBalancers"] == nil {
						t.Fatal("HTTP service lacks target group")
					}
				}
				dir := filepath.Join(t.TempDir(), "artifacts")
				if err = WritePlan(dir, p); err != nil {
					t.Fatal(err)
				}
				if err = WritePlan(dir, p); err == nil {
					t.Fatal("overwrote output")
				}
				got, err := os.ReadFile(filepath.Join(dir, "plan.json"))
				if err != nil || !bytes.Equal(got, a) {
					t.Fatal("existing plan changed", err)
				}
			})
		}
	}
}
func TestOfflineCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // No go, cloud CLI or network command can execute.
	file := filepath.Join(t.TempDir(), "blossm.json")
	ctx := context.Background()
	var out, stderr bytes.Buffer
	if err := Run(ctx, []string{"init", "--config", file}, &out, &stderr); err != nil {
		t.Fatal(err)
	}
	if err := Run(ctx, []string{"init", "--config", file}, &out, &stderr); err == nil {
		t.Fatal("init overwrote file")
	}
	for _, args := range [][]string{{"validate", "--config", file}, {"plan", "--config", file}, {"deploy", "--dry-run", "--config", file}, {"generate", "--config", file, "--out", filepath.Join(t.TempDir(), "plan")}} {
		if err := Run(ctx, args, &out, &stderr); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"deploy", "--execute", "--dry-run", "--config", file}, {"unknown"}, {"validate", "--config", file, "ignored"}} {
		if err := Run(ctx, args, &out, &stderr); err == nil {
			t.Fatal("invalid command succeeded", args)
		}
	}
}
func TestSubprocessFailureAndOfflineEnvironment(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	data, _ := encode(Example("gcp", "postgres"))
	if err := os.WriteFile(filepath.Join(dir, "blossm.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RECORD", record)
	script := "#!/bin/sh\nprintf '%s\\n' \"$GOPROXY\" \"$GOSUMDB\" \"$GOTOOLCHAIN\" \"$GOWORK\" \"$@\" > \"$RECORD\"\nexit 17\n"
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	for _, command := range []string{"test", "build"} {
		t.Run(command, func(t *testing.T) {
			var out bytes.Buffer
			err := Run(context.Background(), []string{command, "--dir", dir, "--out", filepath.Join(dir, "bin")}, &out, &out)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 17 {
				t.Fatalf("lost failure: %v", err)
			}
			record, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(record), "off\noff\nlocal\noff\n"+command+"\n") {
				t.Fatalf("wrong offline process environment: %s", record)
			}
		})
	}
}
func TestNativePrerequisitesFailClosed(t *testing.T) {
	t.Setenv("POSTGRES_URL", "")
	t.Setenv("MONGODB_URI", "")
	var out bytes.Buffer
	err := Run(context.Background(), []string{"test", "--integration"}, &out, &out)
	if err == nil || !strings.Contains(err.Error(), "POSTGRES_URL and MONGODB_URI") {
		t.Fatal(err)
	}
}

// This invokes an actual Go test binary with an intentional assertion failure.
func TestRealGoFailurePropagation(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, data := range map[string]string{"go.mod": "module failure.test\n\ngo 1.26.0\n", "failure_test.go": "package failure\nimport \"testing\"\nfunc TestFailure(t *testing.T){t.Fatal(\"intentional-cli-fixture\")}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := Run(context.Background(), []string{"test", "--dir", dir}, &out, &out)
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == 0 || !strings.Contains(out.String(), "intentional-cli-fixture") {
		t.Fatalf("test failure was swallowed: %v %s", err, out.String())
	}
}

func TestCheckedExamples(t *testing.T) {
	for _, example := range []struct{ name, provider, driver string }{{"gcp-postgres", "gcp", "postgres"}, {"aws-mongo", "aws", "mongo"}} {
		data, err := os.ReadFile(filepath.Join("..", "deploy", "examples", example.name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		expected, err := encode(Example(example.provider, example.driver))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatalf("example %s drifted from init", example.name)
		}
		if _, err = DecodeConfig(bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeSecretsAndPublication(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		c := Example(provider, "postgres")
		p, err := Prepare(c)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := encode(p)
		for _, key := range []string{"API_KEY", "PUBLICATION_TOPIC"} {
			if !bytes.Contains(b, []byte(key)) {
				t.Fatalf("missing %s in %s", key, provider)
			}
		}
		if provider == "gcp" && !bytes.Contains(b, []byte("PUBLICATION_SUBSCRIPTION")) {
			t.Fatal("missing subscription")
		}
		if provider == "aws" && !bytes.Contains(b, []byte("PUBLICATION_QUEUE_URL")) {
			t.Fatal("missing queue")
		}
		c.Env = map[string]string{"PUBLICATION_TOPIC": "override"}
		if err = c.Validate(); err == nil {
			t.Fatal("accepted protected publication override")
		}
		c = Example(provider, "postgres")
		c.Publication = &Publication{}
		if err = c.Validate(); err == nil {
			t.Fatal("accepted missing publication config")
		}
	}
}

func TestCloudRunBackgroundProgress(t *testing.T) {
	p, err := Prepare(Example("gcp", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	var service struct {
		Spec struct {
			Template struct {
				Metadata struct{ Annotations map[string]string }
			}
		}
	}
	if err = json.Unmarshal(p.Artifacts["cloud-run.json"], &service); err != nil {
		t.Fatal(err)
	}
	a := service.Spec.Template.Metadata.Annotations
	if a["autoscaling.knative.dev/minScale"] != "1" || a["run.googleapis.com/cpu-throttling"] != "false" {
		t.Fatal("background delivery can stop while idle", a)
	}
}

func TestImagePins(t *testing.T) {
	for _, image := range []string{"repo:latest", "repo:", "https://example.test/repo:tag", "repo@sha256:short", "repo@sha256:" + strings.Repeat("z", 64)} {
		if validImage(image) {
			t.Fatal("accepted invalid image pin", image)
		}
	}
	for _, image := range []string{"example.test/repo:v1", "repo@sha256:" + strings.Repeat("a", 64)} {
		if !validImage(image) {
			t.Fatal("rejected pin", image)
		}
	}
}
