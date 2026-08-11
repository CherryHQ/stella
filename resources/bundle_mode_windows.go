//go:build windows

package resources

import "io/fs"

func bundleFileModeMatches(actual, expected fs.FileMode) bool {
	// Windows does not preserve Unix executable bits: writable ordinary files
	// report 0666 and read-only files report 0444. Content digests still verify
	// exact bytes, so compare the only portable permission distinction.
	return writableBundleModeMatches(actual, expected)
}

func bundleDirectoryModeMatches(fs.FileMode) bool {
	// Directory traversal is controlled by Windows ACLs rather than Unix mode
	// bits, and Go does not preserve a requested 0755 directory mode.
	return true
}
