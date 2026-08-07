//go:build windows

package vision

import (
	"errors"
	"os/exec"
)

var errXbergUnsupportedPlatform = errors.New("daemon Vision Xberg fallback is unsupported on Windows")

func xbergFallbackSupported() error { return errXbergUnsupportedPlatform }

// Windows fails before process startup because taskkill cannot provide the
// POSIX process-group containment required for daemon-side staging cleanup.
func terminateXbergProcessTree(*exec.Cmd) error { return errXbergUnsupportedPlatform }

func confirmXbergProcessGroupGone(int) error { return errXbergUnsupportedPlatform }
