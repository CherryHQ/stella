package dockerclient

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupOrphanedContainers_RemovesExited(t *testing.T) {
	tmp := t.TempDir()

	// Write a more complex shim for orphan tests that needs multiple sub-commands.
	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$*" in
  *"ps --all"*)
    printf 'cont1\n'
    exit 0
    ;;
  *"inspect"*"cont1"*)
    printf 'exited 2024-01-01T00:00:00Z\n'
    exit 0
    ;;
  *"rm --force cont1"*)
    exit 0
    ;;
esac
exit 0
`, filepath.Join(tmp, "docker.log"))

	shimPath := filepath.Join(tmp, "docker")
	if err := os.WriteFile(shimPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	c := NewWithPath(shimPath)
	CleanupOrphanedContainers(context.Background(), c, "/home/.anna")

	log := readLog(t, tmp)
	if !strings.Contains(log, "rm --force") {
		t.Errorf("expected rm --force in log, got:\n%s", log)
	}
}

func TestCleanupOrphanedContainers_RemovesOldContainer(t *testing.T) {
	tmp := t.TempDir()

	// Container is "running" but more than 1 hour old.
	oldTime := time.Now().Add(-2 * time.Hour).Format(time.RFC3339)

	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$*" in
  *"ps --all"*)
    printf 'old1\n'
    exit 0
    ;;
  *"inspect"*"old1"*)
    printf 'running %s\n'
    exit 0
    ;;
  *"rm --force old1"*)
    exit 0
    ;;
esac
exit 0
`, filepath.Join(tmp, "docker.log"), oldTime)

	shimPath := filepath.Join(tmp, "docker")
	if err := os.WriteFile(shimPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	c := NewWithPath(shimPath)
	CleanupOrphanedContainers(context.Background(), c, "/home/.anna")

	log := readLog(t, tmp)
	if !strings.Contains(log, "rm --force") {
		t.Errorf("expected rm --force for old container, got:\n%s", log)
	}
}

func TestCleanupOrphanedContainers_KeepsYoungRunning(t *testing.T) {
	tmp := t.TempDir()

	recentTime := time.Now().Add(-5 * time.Minute).Format(time.RFC3339)

	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$*" in
  *"ps --all"*)
    printf 'live1\n'
    exit 0
    ;;
  *"inspect"*"live1"*)
    printf 'running %s\n'
    exit 0
    ;;
esac
exit 0
`, filepath.Join(tmp, "docker.log"), recentTime)

	shimPath := filepath.Join(tmp, "docker")
	if err := os.WriteFile(shimPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	c := NewWithPath(shimPath)
	CleanupOrphanedContainers(context.Background(), c, "/home/.anna")

	log := readLog(t, tmp)
	if strings.Contains(log, "rm --force") {
		t.Errorf("should NOT remove young running container, got:\n%s", log)
	}
}

func TestCleanupOrphanedContainers_EmptyList(t *testing.T) {
	tmp := t.TempDir()
	shimPath := writeShim(t, tmp, []shimCase{
		{match: "ps --all", exitCode: 0, stdout: ""},
	})

	c := NewWithPath(shimPath)
	// Should complete without error.
	CleanupOrphanedContainers(context.Background(), c, "/home/.anna")

	log := readLog(t, tmp)
	if strings.Contains(log, "rm --force") {
		t.Errorf("should not call rm with empty list, got:\n%s", log)
	}
}

func TestCleanupOrphanedContainers_ToleratesMissingContainer(t *testing.T) {
	tmp := t.TempDir()

	scriptContent := fmt.Sprintf(`#!/bin/sh
echo "$@" >> %q
case "$*" in
  *"ps --all"*)
    printf 'gone1\n'
    exit 0
    ;;
  *"inspect"*"gone1"*)
    printf 'Error: No such container: gone1\n' >&2
    exit 1
    ;;
esac
exit 0
`, filepath.Join(tmp, "docker.log"))

	shimPath := filepath.Join(tmp, "docker")
	if err := os.WriteFile(shimPath, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write shim: %v", err)
	}

	c := NewWithPath(shimPath)
	// Should not panic or fail.
	CleanupOrphanedContainers(context.Background(), c, "/home/.anna")
}
