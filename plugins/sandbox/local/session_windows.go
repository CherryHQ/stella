//go:build windows

package local

import (
	"fmt"
	"os/exec"
)

// setSysProcAttr is a no-op on Windows; SysProcAttr fields differ.
func setSysProcAttr(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func waitProcessGroupAbsent(_ int) error {
	return fmt.Errorf("local: cannot prove process-tree absence on Windows")
}

func processTreeSupported() bool { return false }

// applyRlimits is a no-op on Windows; rlimit is not supported.
func applyRlimits(_ *exec.Cmd) error { return nil }
