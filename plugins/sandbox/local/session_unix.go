//go:build !windows && !linux

package local

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
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
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func waitProcessGroupAbsent(cmd *exec.Cmd) error {
	pgid := 0
	if cmd.Process != nil {
		pgid = cmd.Process.Pid
	}
	if pgid <= 0 {
		return fmt.Errorf("local: invalid process group")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("local: prove process group %d absent: %w", pgid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("local: process group %d still exists", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

//nolint:unused // selected by session_other.go on non-darwin Unix targets.
func processTreeSupported() bool { return true }

// applyRlimits is a no-op on non-Linux platforms. On Linux, resource limits
// are applied via prlimit(2) after the process starts.
func applyRlimits(_ *exec.Cmd) error { return nil }
