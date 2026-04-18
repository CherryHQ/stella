package dockerclient

import (
	"context"
	"strings"
	"testing"
)

func TestCreateAndStart_BasicArgv(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "create", exitCode: 0, stdout: "abc123\n"},
		{match: "start", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	opts := CreateOptions{
		Image:          "ubuntu:22.04",
		WorkspaceHost:  "/host/ws",
		WorkspaceMount: "/workspace",
		NetworkMode:    NetworkDisabled,
		Labels: map[string]string{
			LabelSessionID: "sess-1",
			LabelAnnaHome:  "/home/.anna",
			LabelCreatedAt: "2024-01-01T00:00:00Z",
		},
		Name: "anna-sandbox-sess-1",
	}

	id, err := c.CreateAndStart(context.Background(), opts)
	if err != nil {
		t.Fatalf("CreateAndStart: %v", err)
	}
	if id != "abc123" {
		t.Errorf("container ID = %q; want abc123", id)
	}

	log := readLog(t, tmp)
	if !strings.Contains(log, "--network none") {
		t.Errorf("expected --network none in argv, got:\n%s", log)
	}
	if !strings.Contains(log, LabelSessionID+"=sess-1") {
		t.Errorf("expected label in argv, got:\n%s", log)
	}
	if !strings.Contains(log, "--mount") {
		t.Errorf("expected --mount in argv, got:\n%s", log)
	}
	if !strings.Contains(log, "--entrypoint /bin/sh") {
		t.Errorf("expected entrypoint in argv, got:\n%s", log)
	}
	if !strings.Contains(log, "tail -f /dev/null") {
		t.Errorf("expected tail -f /dev/null in argv, got:\n%s", log)
	}
}

func TestCreateAndStart_AllowAllNetwork(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "create", exitCode: 0, stdout: "def456\n"},
		{match: "start", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	opts := CreateOptions{
		Image:       "ubuntu:22.04",
		NetworkMode: NetworkAllowAll,
		Labels:      map[string]string{LabelSessionID: "sess-2"},
	}

	_, err := c.CreateAndStart(context.Background(), opts)
	if err != nil {
		t.Fatalf("CreateAndStart: %v", err)
	}

	log := readLog(t, tmp)
	// NetworkAllowAll omits --network flag
	if strings.Contains(log, "--network") {
		t.Errorf("expected no --network flag for allow_all, got:\n%s", log)
	}
}

func TestCreateAndStart_WithUser(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "create", exitCode: 0, stdout: "ghi789\n"},
		{match: "start", exitCode: 0},
	})

	c := NewWithPath(shimPath)
	opts := CreateOptions{
		Image:  "ubuntu:22.04",
		User:   "1000:1000",
		Labels: map[string]string{LabelSessionID: "sess-3"},
	}

	_, err := c.CreateAndStart(context.Background(), opts)
	if err != nil {
		t.Fatalf("CreateAndStart: %v", err)
	}

	log := readLog(t, tmp)
	if !strings.Contains(log, "--user 1000:1000") {
		t.Errorf("expected --user 1000:1000 in argv, got:\n%s", log)
	}
}

func TestStop_IdempotentOnMissingContainer(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "stop", exitCode: 1, stderr: "Error response from daemon: No such container: dead123"},
	})

	c := NewWithPath(shimPath)
	// Should not return an error.
	if err := c.Stop(context.Background(), "dead123"); err != nil {
		t.Fatalf("Stop should be idempotent on missing container, got: %v", err)
	}
}

func TestStop_ReturnsErrorOnOtherFailure(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "stop", exitCode: 1, stderr: "daemon is not running"},
	})

	c := NewWithPath(shimPath)
	if err := c.Stop(context.Background(), "abc123"); err == nil {
		t.Fatal("expected error on stop failure")
	}
}

func TestContainerAlive_True(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "inspect", exitCode: 0, stdout: "true\n"},
	})

	c := NewWithPath(shimPath)
	alive, err := c.ContainerAlive(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("ContainerAlive: %v", err)
	}
	if !alive {
		t.Error("expected alive=true")
	}
}

func TestContainerAlive_False(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "inspect", exitCode: 0, stdout: "false\n"},
	})

	c := NewWithPath(shimPath)
	alive, err := c.ContainerAlive(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("ContainerAlive: %v", err)
	}
	if alive {
		t.Error("expected alive=false")
	}
}

func TestContainerAlive_MissingContainer(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "inspect", exitCode: 1, stderr: "Error: No such container: missing"},
	})

	c := NewWithPath(shimPath)
	alive, err := c.ContainerAlive(context.Background(), "missing")
	if err != nil {
		t.Fatalf("ContainerAlive on missing container: %v", err)
	}
	if alive {
		t.Error("expected alive=false for missing container")
	}
}
