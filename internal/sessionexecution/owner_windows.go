//go:build windows

package sessionexecution

import (
	"golang.org/x/sys/windows"
)

// stillActive is the Win32 STILL_ACTIVE pseudo exit code (STATUS_PENDING);
// x/sys/windows does not export it.
const stillActive = 259

// pidAlive reports whether pid currently names a live process: the handle must
// open AND the process must still be running (a lingering handle can keep a
// dead pid openable).
func pidAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
