//go:build windows

package manifest

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
)

func managedCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Cancel = func() error {
		if cmd.Process == nil || cmd.ProcessState != nil {
			return os.ErrProcessDone
		}
		pid := strconv.Itoa(cmd.Process.Pid)
		err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run()
		if err == nil {
			return nil
		}
		killErr := cmd.Process.Kill()
		if killErr == nil || errors.Is(killErr, os.ErrProcessDone) {
			return nil
		}
		return err
	}
	cmd.WaitDelay = managedCommandWaitDelay
	return cmd
}
