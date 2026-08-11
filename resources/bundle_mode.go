package resources

import "io/fs"

func writableBundleModeMatches(actual, expected fs.FileMode) bool {
	return actual.Perm()&0o222 != 0 == (expected.Perm()&0o222 != 0)
}
