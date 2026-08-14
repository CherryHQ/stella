//go:build !linux && !darwin

package sessionfs

import (
	"errors"
	"os"
)

func renameRootNoReplace(*os.Root, string, string) error {
	return errors.New("sandbox: atomic no-replace projection is unsupported on this platform")
}
