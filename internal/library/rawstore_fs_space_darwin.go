//go:build darwin

package library

import "syscall"

func availableDiskBytes(path string) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	// Darwin exposes Bsize as uint32, unlike Linux's int64 field.
	return int64(stats.Bavail) * int64(stats.Bsize), nil
}
