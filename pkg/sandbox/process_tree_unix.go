//go:build !windows

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// On POSIX the process tree fence is the process group: the child leads its own
// group, so a single negative-PID signal reaches every descendant, and probing
// that group with signal 0 is the proof that nothing is left. Windows has no
// equivalent and carries the same contract through Job Objects instead.

// treeAbsenceTimeout bounds how long absence is polled after SIGKILL. A caller
// that cannot prove absence in time keeps the resource alive rather than
// assuming it is gone.
const treeAbsenceTimeout = 5 * time.Second

// SetProcessTreeSysProcAttr prepares cmd so that its descendants stay inside one
// killable process group.
func SetProcessTreeSysProcAttr(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// KillProcessTree SIGKILLs every process in the group led by cmd.
func KillProcessTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// WaitProcessTreeAbsent blocks until no process remains in the group led by cmd,
// and returns an error when that cannot be proven within the deadline.
func WaitProcessTreeAbsent(cmd *exec.Cmd) error {
	pgid := 0
	if cmd.Process != nil {
		pgid = cmd.Process.Pid
	}
	if pgid <= 0 {
		return fmt.Errorf("sandbox: invalid process group")
	}
	deadline := time.Now().Add(treeAbsenceTimeout)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		// EPERM means the group still exists but is owned by somebody else.
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("sandbox: prove process group %d absent: %w", pgid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("sandbox: process group %d still exists", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
