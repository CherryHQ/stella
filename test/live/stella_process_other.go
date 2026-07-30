//go:build !unix

package live

import (
	"os"
	"os/exec"
)

// Release Live runs on Linux, but these conservative stubs keep the package
// buildable on hosts without Unix process-group semantics.
func setLiveProcessGroup(*exec.Cmd) {}

func terminateLiveProcess(process *os.Process) error {
	return process.Kill()
}

func killLiveProcessGroup(command *exec.Cmd) {
	if command != nil && command.Process != nil {
		_ = command.Process.Kill()
	}
}

func liveProcessGroupAlive(*exec.Cmd) bool {
	return false
}
