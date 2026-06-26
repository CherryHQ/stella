package ml

import (
	"os/exec"
	"syscall"
)

// setParentDeathSignal asks the kernel to SIGKILL the sidecar if stellad dies, so
// a hard-killed parent never leaves an orphaned sidecar holding the socket.
func setParentDeathSignal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}
