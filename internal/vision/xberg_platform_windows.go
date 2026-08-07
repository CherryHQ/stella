//go:build windows

package vision

import "errors"

var errXbergUnsupportedPlatform = errors.New("daemon Vision Xberg fallback is unsupported on Windows")

func xbergFallbackSupported() error { return errXbergUnsupportedPlatform }
