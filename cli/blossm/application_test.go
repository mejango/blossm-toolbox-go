package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndependentApplicationBuild(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "hello"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"go.mod": "module independent.test/app\n\ngo 1.26.0\n", "cmd/hello/main.go": "package main\nimport \"fmt\"\nfunc main(){fmt.Print(\"independent application\")}\n"}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := Example("gcp", "postgres")
	c.Database = nil
	c.Publication = nil
	c.Secrets = nil
	c.Application = Application{Package: "./cmd/hello", Binary: "hello", Entrypoint: "/app/hello"}
	data, _ := encode(c)
	if err := os.WriteFile(filepath.Join(root, "blossm.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"build", "--dir", root}, &out, &out); err != nil {
		t.Fatal(err, out.String())
	}
	got, err := exec.Command(filepath.Join(root, "bin", "hello")).Output()
	if err != nil || string(got) != "independent application" {
		t.Fatal(err, string(got))
	}
	if err = os.WriteFile(filepath.Join(root, "cmd", "hello", "main.go"), []byte("invalid Go"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Run(context.Background(), []string{"build", "--dir", root}, &out, &out); err == nil {
		t.Fatal("compilation failure hidden")
	}
}
func TestOptionalInfrastructureAndEnvironmentOwnership(t *testing.T) {
	for _, provider := range []string{"gcp", "aws"} {
		c := Example(provider, "postgres")
		c.Database = nil
		c.Publication = nil
		c.Secrets = nil
		c.Env = map[string]string{"GREETING": "hello", "NETWORK": "app-network"}
		p, err := Prepare(c)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"DATABASE_DRIVER", "DATABASE_NAMESPACE", "PUBLICATION_TOPIC", "JWT_SIGNER", "LOCAL_MODE", "PUBLISHER"} {
			if _, ok := c.RuntimeEnv()[key]; ok {
				t.Fatal("invented integration", key)
			}
		}
		if len(p.PreparationCommands) != 0 || p.Artifacts["sns-publication-subscription.json"] != nil {
			t.Fatal("invented publication setup")
		}
		if c.RuntimeEnv()["GREETING"] != "hello" {
			t.Fatal("application env lost")
		}
		for _, key := range []string{"PORT", "GCP_PROJECT", "AWS_REGION", "REPLAY_RUN_ID"} {
			c.Env = map[string]string{key: "override"}
			if c.Validate() == nil {
				t.Fatal("protected key", key)
			}
		}
		c = Example(provider, "postgres")
		c.Env = map[string]string{"API_KEY": "plaintext"}
		if c.Validate() == nil {
			t.Fatal("env/secret collision")
		}
		c = Example(provider, "postgres")
		c.Env = map[string]string{"DATABASE_DRIVER": "other"}
		if c.Validate() == nil {
			t.Fatal("database override")
		}
		c.Env = nil
		c.Peers = &Peers{Network: "example.test", Service: "sample"}
		c.Secrets["DELEGATED_SUBJECT"] = c.Secrets["API_KEY"]
		if c.Validate() == nil {
			t.Fatal("peer secret override")
		}
	}
}
func TestApplicationPaths(t *testing.T) {
	for _, pkg := range []string{"../escape", "./cmd/../outside", "-trimpath", "example.test/remote", "./cmd/...", "./cmd//server", ""} {
		c := Example("gcp", "postgres")
		c.Application.Package = pkg
		if c.Validate() == nil {
			t.Fatal("unsafe package", pkg)
		}
	}
	for _, binary := range []string{"../server", "/server", "-server", "a b"} {
		c := Example("gcp", "postgres")
		c.Application.Binary = binary
		if c.Validate() == nil {
			t.Fatal("unsafe binary", binary)
		}
	}
	c := Example("gcp", "postgres")
	c.Application.Entrypoint = "/other"
	if c.Validate() == nil {
		t.Fatal("inconsistent entrypoint")
	}
	c.Application.Entrypoint = "/server"
	c.Application.Package = "."
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestPeerURLAndIdentitySecurity(t *testing.T) {
	for _, target := range []string{"https://child.example/?token=secret", "https://child.example/#fragment", "https://user@child.example/", "http://child.example/"} {
		c := Example("gcp", "postgres")
		c.Peers = &Peers{Network: "example.test", Service: "sample", OutboundURL: target, OutboundService: "child"}
		if c.Validate() == nil {
			t.Fatal("unsafe peer", target)
		}
	}
	for _, network := range []string{"", "a/b", "a\\b", "a b"} {
		c := Example("gcp", "postgres")
		c.Peers = &Peers{Network: network, Service: "sample"}
		if c.Validate() == nil {
			t.Fatal("ambiguous network", network)
		}
	}
	c := Example("gcp", "postgres")
	c.Peers = &Peers{Network: "example.test", Service: "sample", DelegatedSubject: "elsewhere/peer"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "configured network") {
		t.Fatal(err)
	}
}
