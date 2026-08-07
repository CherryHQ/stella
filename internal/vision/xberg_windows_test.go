//go:build windows

package vision

// POSIX-shell Xberg process tests are skipped on Windows.
func processGone(int) bool { return true }

func killProcessGroup(int) {}
