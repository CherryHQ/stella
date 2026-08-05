//go:build linux

package fsops

import (
	"os"
	"runtime"

	"golang.org/x/sys/unix"
)

func renameNoReplace(oldParent *os.File, oldName string, newParent *os.File, newName string) error {
	err := unix.Renameat2(int(oldParent.Fd()), oldName, int(newParent.Fd()), newName, unix.RENAME_NOREPLACE)
	runtime.KeepAlive(oldParent)
	runtime.KeepAlive(newParent)
	return err
}
