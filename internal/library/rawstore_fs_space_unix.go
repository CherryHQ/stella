//go:build !windows && !darwin

package library

import "syscall"

func availableDiskBytes(path string) (int64, error) {
	var stats syscall.Statfs_t
	if err := syscall.Statfs(path, &stats); err != nil {
		return 0, err
	}
	return int64(stats.Bavail) * stats.Bsize, nil
}
