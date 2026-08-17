//go:build !windows

package none

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func waitProcessGroupAbsent(pgid int) error {
	if pgid <= 0 {
		return fmt.Errorf("none: invalid process group")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("none: prove process group %d absent: %w", pgid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("none: process group %d still exists after SIGKILL", pgid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
