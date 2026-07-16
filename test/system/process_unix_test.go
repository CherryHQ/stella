//go:build system && unix

package system

import (
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup makes the subprocess its own process-group leader, so
// teardown can address the server and every descendant it spawns as one unit.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminate sends the platform's graceful termination signal, giving the
// server its normal drain path before any forced kill.
func terminate(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}

// killProcessGroup force-kills the subprocess's whole process group. Setpgid
// made the child the group leader, so its pid addresses the group even after
// the child itself has been reaped.
func killProcessGroup(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}

// processGroupAlive reports whether any process remains in the subprocess's
// group. Signal 0 probes without killing; ESRCH means the group is empty.
// This checks only PIDs the suite owns — never a global scan by process name,
// which could confuse an operator's own stellad for a leak.
func processGroupAlive(cmd *exec.Cmd) bool {
	return syscall.Kill(-cmd.Process.Pid, 0) == nil
}
