//go:build windows

package none

import (
	"os/exec"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// setSysProcAttr marks the child for Job Object fencing. On Windows the process
// tree is owned by a job created at start time, not by a process group.
func setSysProcAttr(cmd *exec.Cmd) {
	sandboxpkg.SetProcessTreeSysProcAttr(cmd)
}

// killProcessGroup terminates the whole job, which is every descendant of cmd.
func killProcessGroup(cmd *exec.Cmd) {
	sandboxpkg.KillProcessTree(cmd)
}

// waitProcessGroupAbsent proves that the job of cmd holds no process left.
func waitProcessGroupAbsent(cmd *exec.Cmd) error {
	return sandboxpkg.WaitProcessTreeAbsent(cmd)
}
