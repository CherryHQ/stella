//go:build darwin

package sessionfs

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameRootNoReplace(root *os.Root, oldName, newName string) error {
	return renameRootParents(root, oldName, newName, func(oldFD int, oldBase string, newFD int, newBase string) error {
		return unix.RenameatxNp(oldFD, oldBase, newFD, newBase, unix.RENAME_EXCL)
	})
}
