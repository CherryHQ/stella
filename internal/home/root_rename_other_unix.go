//go:build unix && !linux && !darwin

package home

import (
	"errors"
	"os"
)

func renameRootNoReplace(*os.Root, string, string) error {
	return errors.New("home: no-replace rename is unsupported on this Unix platform")
}
