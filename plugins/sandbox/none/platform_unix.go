//go:build !windows

package none

import (
	"os/exec"
	"syscall"
)

func shell() (string, string) { return "sh", "-c" }

func platformAvailable() bool { return true }

// setSysProcAttr places the child in its own process group so Close can
// cancel shell descendants together with the leader.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
}

// killProcessGroup cancels the owned process group while its leader is live.
// Process.Signal checks completion safely against Wait; reading ProcessState races.
// This best-effort cancellation does not replace resource absence verification.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
