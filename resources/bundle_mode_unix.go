//go:build !windows

package resources

import "io/fs"

func bundleFileModeMatches(actual, expected fs.FileMode) bool {
	return actual.Perm() == expected.Perm()
}

func bundleDirectoryModeMatches(actual fs.FileMode) bool {
	return actual.Perm() == 0o755
}
