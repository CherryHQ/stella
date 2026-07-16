//go:build system && !unix

package system

import (
	"os"
	"os/exec"
)

// Non-unix hosts have no published embedded PostgreSQL runtime, so the suite
// skips before starting a subprocess. These stubs only keep the package
// compiling under `-tags system` on such hosts; they run for the early-exit
// harness test at most, where plain Kill semantics are sufficient.

func setProcessGroup(*exec.Cmd) {}

func terminate(p *os.Process) error {
	return p.Kill()
}

func killProcessGroup(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
}

func processGroupAlive(*exec.Cmd) bool {
	return false
}
