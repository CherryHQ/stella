package docker

import (
	"context"
	"strings"
	"testing"

	"github.com/vaayne/anna/plugins/sandbox/docker/dockerclient"
)

// TestPreflightImageExists verifies that Preflight succeeds when the image is already local.
func TestPreflightImageExists(t *testing.T) {
	tmpdir := t.TempDir()
	shimPath := writeShim(t, tmpdir, []shimCase{
		{match: "version", exitCode: 0, stdout: `{"Client":{"Version":"24.0.0"},"Server":{"ApiVersion":"1.43"}}`},
		{match: "image inspect", exitCode: 0, stdout: "[]"},
	})

	client := dockerclient.NewWithPath(shimPath)
	cfg := PreflightConfig{
		Docker: Config{Image: "alpine:3.20"},
	}

	if err := preflightWithClient(context.Background(), cfg, client); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

// TestPreflightImageMissingNoPull verifies error when image is missing and AllowPull=false.
func TestPreflightImageMissingNoPull(t *testing.T) {
	tmpdir := t.TempDir()
	shimPath := writeShim(t, tmpdir, []shimCase{
		{match: "version", exitCode: 0, stdout: `{"Client":{"Version":"24.0.0"},"Server":{"ApiVersion":"1.43"}}`},
		// image inspect exits 1 (not found)
		{match: "image inspect", exitCode: 1, stderr: "Error: No such image: alpine:3.20"},
	})

	client := dockerclient.NewWithPath(shimPath)
	cfg := PreflightConfig{
		Docker: Config{Image: "alpine:3.20", AllowPull: false},
	}

	err := preflightWithClient(context.Background(), cfg, client)
	if err == nil {
		t.Fatal("expected error when image missing and AllowPull=false")
	}
	if !strings.Contains(err.Error(), "AllowPull") {
		t.Errorf("expected error mentioning AllowPull, got: %v", err)
	}
}

// TestPreflightImageMissingWithPull verifies that Preflight pulls when AllowPull=true.
func TestPreflightImageMissingWithPull(t *testing.T) {
	tmpdir := t.TempDir()
	shimPath := writeShim(t, tmpdir, []shimCase{
		{match: "version", exitCode: 0, stdout: `{"Client":{"Version":"24.0.0"},"Server":{"ApiVersion":"1.43"}}`},
		// image inspect exits 1 on first call
		{match: "image inspect", exitCode: 1, stderr: "Error: No such image: myimage:latest"},
		// pull succeeds
		{match: "pull", exitCode: 0},
	})

	client := dockerclient.NewWithPath(shimPath)
	cfg := PreflightConfig{
		Docker: Config{Image: "myimage:latest", AllowPull: true},
	}

	if err := preflightWithClient(context.Background(), cfg, client); err != nil {
		t.Fatalf("expected no error with AllowPull=true, got: %v", err)
	}

	log := readLog(t, tmpdir)
	if !strings.Contains(log, "pull") {
		t.Errorf("expected docker pull to be invoked, log: %s", log)
	}
}

// TestPreflightDaemonUnreachable verifies error when daemon is not reachable.
func TestPreflightDaemonUnreachable(t *testing.T) {
	tmpdir := t.TempDir()
	shimPath := writeShim(t, tmpdir, []shimCase{
		{match: "version", exitCode: 1, stderr: "Cannot connect to Docker daemon"},
	})

	client := dockerclient.NewWithPath(shimPath)
	cfg := PreflightConfig{
		Docker: Config{Image: "alpine:3.20"},
	}

	err := preflightWithClient(context.Background(), cfg, client)
	if err == nil {
		t.Fatal("expected error when daemon unreachable")
	}
	if !strings.Contains(err.Error(), "daemon not reachable") {
		t.Errorf("expected 'daemon not reachable' in error, got: %v", err)
	}
}
