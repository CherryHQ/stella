//go:build !darwin && !linux

package fsops

import (
	"errors"
	"os"
)

func renameNoReplace(_ *os.File, _ string, _ *os.File, _ string) error {
	// Deliberate ceiling: add a Windows atomic handle-relative rename when
	// Windows workspace moves are supported; never fall back to replace-on-rename.
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
