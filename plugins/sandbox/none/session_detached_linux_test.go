//go:build linux

package none

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// TestFactoryCreateSessionDetachedOrphanRetainsTemp proves the none backend
// cannot remove its owned temp dir after the tracked leader exits while a
// detached descendant is still alive. The PID-file handshake makes the
// descendant state deterministic instead of relying on a sleep race.
func TestFactoryCreateSessionDetachedOrphanRetainsTemp(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skipf("setsid unavailable: %v", err)
	}
	workingDir := t.TempDir()
	sess, err := NewFactoryWithMountSources(nil, Config{}).CreateSession(context.Background(), sandboxpkg.Policy{
		Filesystem: sandboxpkg.FilesystemPolicy{WorkingDir: workingDir},
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := sess.(*noneSession)
	t.Cleanup(func() { _ = s.Close() })
	tmpDir := s.ownedTempDir
	if tmpDir == "" {
		t.Fatal("session did not allocate an owned temp directory")
	}
	pidFile := filepath.Join(t.TempDir(), "detached.pid")
	pid := 0
	t.Cleanup(func() {
		if pid == 0 {
			if data, readErr := os.ReadFile(pidFile); readErr == nil {
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			}
		}
		if pid > 0 {
			// setsid makes the shell's PID the process-group/session ID. Kill the
			// whole dedicated group so the 60-second child cannot outlive the test.
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		_ = os.Remove(pidFile)
		_ = s.Close()
	})
	// Keep the child alive long enough to observe both sides of Close. The
	// process is in its own session and is killed in Cleanup by process group.
	command := `setsid sh -c 'printf "%s\n" "$$" > "$STELLA_TEST_DETACHED_PID"; sleep 60' >/dev/null 2>&1 & exit 0`
	if _, err := s.Exec(context.Background(), command, sandboxpkg.ExecOptions{
		Env: map[string]string{"STELLA_TEST_DETACHED_PID": pidFile},
	}); err != nil {
		t.Fatalf("start detached child: %v", err)
	}
	pid = waitForDetachedPID(t, pidFile)
	assertProcessAlive(t, pid, "before Close")

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	assertProcessAlive(t, pid, "after Close")
	if _, err := os.Stat(tmpDir); err != nil {
		t.Fatalf("temp directory removed without a descendant proof: %v", err)
	}
}

func waitForDetachedPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if parseErr == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("detached child did not publish a valid PID in %s", path)
	return 0
}

func assertProcessAlive(t *testing.T, pid int, phase string) {
	t.Helper()
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached PID %d is not alive %s: %v", pid, phase, err)
	}
}
