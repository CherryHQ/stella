//go:build windows

package none

import (
	"fmt"
	"os/exec"
)

func setSysProcAttr(_ *exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil && cmd.ProcessState == nil {
		_ = cmd.Process.Kill()
	}
}

func waitProcessGroupAbsent(_ int) error {
	return fmt.Errorf("none: cannot prove process-tree absence on Windows")
}
