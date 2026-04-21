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

// killProcessGroup sends SIGKILL to the process group of the given command.
// A negative PID targets the entire process group.
// No-ops when the process has already been reaped (ProcessState != nil).
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.ProcessState == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// applyRlimits is a no-op on non-Linux platforms. On Linux, resource limits
// are applied via prlimit(2) after the process starts.
func applyRlimits(_ *exec.Cmd) error { return nil }
