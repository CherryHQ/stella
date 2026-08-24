//go:build !linux && !windows

package sandbox

import (
	"context"
	"os/exec"
)

func StartProcessRegistered(_ context.Context, cmd *exec.Cmd, _ ProcessRegistrar) error {
	return cmd.Start()
}
