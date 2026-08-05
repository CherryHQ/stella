//go:build darwin || linux

package fsops

import "syscall"

func setTestUmask(mask int) int { return syscall.Umask(mask) }
