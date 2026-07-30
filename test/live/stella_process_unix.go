//go:build unix

package live

import (
	"os"
	"os/exec"
	"syscall"
)

// setLiveProcessGroup makes the exact candidate its own process-group leader,
// so cleanup can address Stella and its embedded PostgreSQL descendants.
func setLiveProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateLiveProcess asks Stella to follow its normal graceful drain path.
func terminateLiveProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}

// killLiveProcessGroup force-kills only the process group created above.
func killLiveProcessGroup(command *exec.Cmd) {
	if command == nil || command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

// liveProcessGroupAlive probes the owned process group without relying on a
// global process-name search that could match an operator's Stella instance.
func liveProcessGroupAlive(command *exec.Cmd) bool {
	if command == nil || command.Process == nil {
		return false
	}
	return syscall.Kill(-command.Process.Pid, 0) == nil
}
