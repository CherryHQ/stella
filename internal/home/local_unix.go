//go:build linux || darwin

package home

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openLocalDirNoFollow(base, locator string) (*os.File, error) {
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range splitLocalPath(locator) {
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), locator), nil
}

func mkdirAllLocalAt(root *os.File, name string, mode os.FileMode) error {
	fd, err := unix.Dup(int(root.Fd()))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, part := range splitLocalPath(name) {
		if err := unix.Mkdirat(fd, part, uint32(mode.Perm())); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func prepareLocalWorkspaceScaffold(base, locator string, dirs []string) error {
	root, err := openLocalDirNoFollow(base, locator)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	for _, dir := range dirs {
		if err := mkdirAllLocalAt(root, dir, 0o755); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}
	return nil
}
