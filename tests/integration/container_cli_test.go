//go:build containerintegration

package integration

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Exercise the published CLI image independently of any application image.
func TestContainerCLI(t *testing.T) {
	image := os.Getenv("CONTAINER_IMAGE")
	if image == "" {
		t.Fatal("CONTAINER_IMAGE required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", image, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "blossm") {
		t.Fatalf("version: %s: %v", out, err)
	}
}
