//go:build !windows && !linux

package local

import (
	"os/exec"
	"syscall"
)

// setSysProcAttr places the child in its own process group so that
// killProcessGroup can terminate the entire subtree.
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

// applyRlimits is a no-op on non-Linux platforms. On Linux, resource limits
// are applied via prlimit(2) after the process starts.
func applyRlimits(_ *exec.Cmd) error { return nil }
