//go:build linux

package home

import (
	"os"

	"golang.org/x/sys/unix"
)

func renameRootNoReplace(root *os.Root, old, new string) error {
	return renameRootParents(root, old, new, func(oldFD int, oldBase string, newFD int, newBase string) error {
		return unix.Renameat2(oldFD, oldBase, newFD, newBase, unix.RENAME_NOREPLACE)
	})
}
