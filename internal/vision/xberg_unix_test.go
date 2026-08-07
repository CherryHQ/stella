//go:build !windows

package vision

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func processGone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}

func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

// TestXbergEscapedDescendantHelper is invoked as a child test binary by the
// escaped-descendant regression. It publishes its PID only after it has left
// the supervisor's process group, then deliberately retains inherited stdout.
func TestXbergEscapedDescendantHelper(t *testing.T) {
	if os.Getenv("STELLA_XBERG_ESCAPED_DESCENDANT_HELPER") != "1" {
		return
	}
	if _, err := syscall.Setsid(); err != nil {
		t.Fatalf("escape supervisor process group: %v", err)
	}
	if err := os.WriteFile(os.Getenv("STELLA_XBERG_ESCAPED_DESCENDANT_READY"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("publish escaped descendant readiness: %v", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestConfirmXbergProcessGroupGoneRejectsLiveGroup(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "while :; do :; done")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start group leader: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		killProcessGroup(pid)
		_ = cmd.Wait()
	})
	oldWait := xbergGroupConfirmWait
	xbergGroupConfirmWait = 20 * time.Millisecond
	t.Cleanup(func() { xbergGroupConfirmWait = oldWait })
	if err := confirmXbergProcessGroupGone(pid); err == nil {
		t.Fatal("live process group passed post-reap confirmation")
	}
	killProcessGroup(pid)
	if err := cmd.Wait(); err == nil {
		t.Fatal("killed group leader Wait error = nil")
	}
	if err := confirmXbergProcessGroupGone(pid); err != nil {
		t.Fatalf("reaped process group confirmation: %v", err)
	}
}
