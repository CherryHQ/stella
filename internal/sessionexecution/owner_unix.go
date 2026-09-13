//go:build unix

package sessionexecution

import (
	"errors"
	"syscall"
)

// pidAlive reports whether pid currently names a live process. ESRCH proves
// exit; EPERM still means the process exists.
func pidAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
