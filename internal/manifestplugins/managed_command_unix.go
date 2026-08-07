//go:build !windows

package manifestplugins

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// ManagedCommandContext returns a command whose cancellation terminates the
// complete process tree and whose WaitDelay bounds blocked descendant pipes.
// Callers must still call Wait exactly once after a successful Start.
func ManagedCommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = managedCommandWaitDelay
	return cmd
}
