//go:build linux

package sessionfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameRootNoReplace(root *os.Root, oldName, newName string) error {
	return renameRootParents(root, oldName, newName, func(oldFD int, oldBase string, newFD int, newBase string) error {
		return unix.Renameat2(oldFD, oldBase, newFD, newBase, unix.RENAME_NOREPLACE)
	})
}
