//go:build windows

package none

import "os/exec"

func shell() (string, string) { return "cmd", "/c" }

func platformAvailable() bool { return false }

// setSysProcAttr is a no-op on Windows; the backend is unavailable there.
func setSysProcAttr(_ *exec.Cmd) {}

// killProcessGroup falls back to the leader on Windows, where process group
// semantics differ and the backend is unavailable.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
