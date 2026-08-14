//go:build unix

package sessionfs

import (
	"os"
	"path/filepath"
)

func withRootParent(root *os.Root, name string, fn func(int, string) error) error {
	parent, base := filepath.Dir(name), filepath.Base(name)
	directory, err := root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer directory.Close() //nolint:errcheck
	file, err := directory.Open(".")
	if err != nil {
		return err
	}
	defer file.Close() //nolint:errcheck
	var operationErr error
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	if err := raw.Control(func(fd uintptr) { operationErr = fn(int(fd), base) }); err != nil {
		return err
	}
	return operationErr
}

func renameRootParents(root *os.Root, oldName, newName string, fn func(int, string, int, string) error) error {
	return withRootParent(root, oldName, func(oldFD int, oldBase string) error {
		return withRootParent(root, newName, func(newFD int, newBase string) error {
			if err := fn(oldFD, oldBase, newFD, newBase); err != nil {
				return &os.LinkError{Op: "rename", Old: oldName, New: newName, Err: err}
			}
			return nil
		})
	})
}
