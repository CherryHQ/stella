//go:build windows

package local

import "os/exec"

// setSysProcAttr is a no-op on Windows; SysProcAttr fields differ.
func setSysProcAttr(_ *exec.Cmd) {}

// killProcessGroup is a no-op on Windows; process group semantics differ.
func killProcessGroup(_ *exec.Cmd) {}

// applyRlimits is a no-op on Windows; rlimit is not supported.
func applyRlimits(_ *exec.Cmd) error { return nil }
