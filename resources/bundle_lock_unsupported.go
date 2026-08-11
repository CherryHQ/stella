//go:build aix || hurd || plan9 || js || wasip1

package resources

import "errors"

func lockBundleInstall(string) (func() error, error) {
	return nil, errors.New("cross-process builtin bundle installation lock is unsupported on this platform")
}
