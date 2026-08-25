//go:build !windows

package none

import (
	"os/exec"

	sandboxpkg "github.com/CherryHQ/stella/pkg/sandbox"
)

// setSysProcAttr puts the child in its own process group so the whole tree can
// be killed and proven absent as one unit.
func setSysProcAttr(cmd *exec.Cmd) {
	sandboxpkg.SetProcessTreeSysProcAttr(cmd)
}

// killProcessGroup terminates every descendant of cmd.
func killProcessGroup(cmd *exec.Cmd) {
	sandboxpkg.KillProcessTree(cmd)
}

// waitProcessGroupAbsent proves that no process of cmd is left.
func waitProcessGroupAbsent(cmd *exec.Cmd) error {
	return sandboxpkg.WaitProcessTreeAbsent(cmd)
}
