package dockerclient

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestVersion_ParsesJSON(t *testing.T) {
	tmp := t.TempDir()
	payload := `{"Client":{"Version":"24.0.5"},"Server":{"ApiVersion":"1.43"}}`
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "version", exitCode: 0, stdout: payload},
	})

	c := NewWithPath(shimPath)
	info, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if info.Client.Version != "24.0.5" {
		t.Errorf("Client.Version = %q; want 24.0.5", info.Client.Version)
	}
	if info.Server.APIVersion != "1.43" {
		t.Errorf("Server.ApiVersion = %q; want 1.43", info.Server.APIVersion)
	}
}

func TestVersion_ReturnsErrorOnNonZeroExit(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "version", exitCode: 1, stderr: "Cannot connect to the Docker daemon"},
	})

	c := NewWithPath(shimPath)
	_, err := c.Version(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("error %q does not contain expected message", err.Error())
	}
}

func TestImageExists_True(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "image inspect", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	ok, err := c.ImageExists(context.Background(), "ubuntu:22.04")
	if err != nil {
		t.Fatalf("ImageExists: %v", err)
	}
	if !ok {
		t.Error("expected true, got false")
	}
}

func TestImageExists_False(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "image inspect", exitCode: 1, stderr: "Error: No such image: ubuntu:22.04"},
	})

	c := NewWithPath(shimPath)
	ok, err := c.ImageExists(context.Background(), "ubuntu:22.04")
	if err != nil {
		t.Fatalf("ImageExists: %v", err)
	}
	if ok {
		t.Error("expected false, got true")
	}
}

func TestPullImage_ForwardsStderr(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "pull", exitCode: 0, stderr: "Pulling from library/ubuntu"},
	})

	c := NewWithPath(shimPath)
	// Should succeed without error; slog output goes to discard during tests.
	if err := c.PullImage(context.Background(), "ubuntu:22.04"); err != nil {
		t.Fatalf("PullImage: %v", err)
	}
	log := readLog(t, tmp)
	if !strings.Contains(log, "pull") {
		t.Errorf("expected pull in log, got: %s", log)
	}
}

func TestNew_UsesEnvVar(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, nil)

	t.Setenv("DOCKER_BIN", shimPath)
	c, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.binaryPath != shimPath {
		t.Errorf("binaryPath = %q; want %q", c.binaryPath, shimPath)
	}
}

func TestNew_ErrorWhenNotFound(t *testing.T) {
	t.Setenv("DOCKER_BIN", "")
	t.Setenv("PATH", os.TempDir()) // no docker here
	_, err := New()
	if err == nil {
		t.Fatal("expected error when docker not on PATH")
	}
}
