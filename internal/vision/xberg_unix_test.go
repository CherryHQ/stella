//go:build !windows

package vision

import "syscall"

func processGone(pid int) bool {
	return syscall.Kill(pid, 0) != nil
}

func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
