//go:build darwin

package fsops

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	err := unix.RenameatxNp(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName, unix.RENAME_EXCL)
	runtime.KeepAlive(oldParent)
	runtime.KeepAlive(newParent)
	return err
}
