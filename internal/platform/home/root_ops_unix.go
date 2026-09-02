//go:build unix

package home

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func withRootParent(root *os.Root, name string, fn func(int, string) error) error {
	parent, base := filepath.Dir(name), filepath.Base(name)
	dir, err := root.OpenRoot(parent)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	f, err := dir.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	var operationErr error
	raw, err := f.SyscallConn()
	if err != nil {
		return err
	}
	if err := raw.Control(func(fd uintptr) { operationErr = fn(int(fd), base) }); err != nil {
		return err
	}
	return operationErr
}

func symlinkRoot(root *os.Root, target, name string) error {
	if err := withRootParent(root, name, func(fd int, base string) error {
		return unix.Symlinkat(target, fd, base)
	}); err != nil {
		return &os.PathError{Op: "symlink", Path: name, Err: err}
	}
	return nil
}

func readlinkRoot(root *os.Root, name string) (string, error) {
	var target string
	err := withRootParent(root, name, func(fd int, base string) error {
		for size := 128; size <= 1<<20; size *= 2 {
			buffer := make([]byte, size)
			n, err := unix.Readlinkat(fd, base, buffer)
			if err != nil {
				return err
			}
			if n < len(buffer) {
				target = string(buffer[:n])
				return nil
			}
		}
		return errors.New("home: symlink target too long")
	})
	if err != nil {
		return "", &os.PathError{Op: "readlink", Path: name, Err: err}
	}
	return target, nil
}

func renameRootParents(root *os.Root, old, new string, fn func(int, string, int, string) error) error {
	return withRootParent(root, old, func(oldFD int, oldBase string) error {
		return withRootParent(root, new, func(newFD int, newBase string) error {
			if err := fn(oldFD, oldBase, newFD, newBase); err != nil {
				return &os.LinkError{Op: "rename", Old: old, New: new, Err: err}
			}
			return nil
		})
	})
}
