//go:build !windows

package manifest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestInstallScopeCancelKillsChildProcessGroup(t *testing.T) {
	stellaHome := t.TempDir()
	binDir := filepath.Join(stellaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}

	childPIDPath := filepath.Join(stellaHome, "child.pid")
	fakeMise := filepath.Join(binDir, "mise")
	fake := `#!/bin/sh
set -eu
case "$1" in
  trust)
    exit 0
    ;;
  install)
    sleep 30 &
    printf '%s\n' "$!" > ` + shellQuote(childPIDPath) + `
    wait
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(fakeMise, []byte(fake), 0o755); err != nil {
		t.Fatalf("write fake mise: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		err := installScope(ctx, stellaHome, builtinScope, []miseTool{{Key: "github:owner/repo", Lookup: "mytool"}})
		errCh <- err
	}()

	pidData, err := waitForFile(childPIDPath, 10*time.Second)
	if err != nil {
		cancel()
		t.Fatalf("read child pid: %v", err)
	}
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("installBinaryWithMise succeeded, want cancellation error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("installBinaryWithMise did not return after cancellation")
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("child process %d still exists after context cancellation", pid)
}

func waitForFile(path string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return nil, lastErr
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || !errors.Is(err, syscall.ESRCH)
}
