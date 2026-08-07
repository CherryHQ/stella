//go:build !windows

package vision

import (
	"os/exec"
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
