package sandbox

import "os"

// EnsurePrivateDir creates path if needed and enforces owner-only permissions.
// MkdirAll does not chmod existing directories, so callers must use this for
// security-sensitive shared host directories.
func EnsurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}
