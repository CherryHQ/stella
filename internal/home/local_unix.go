//go:build linux || darwin

package home

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path"
	"time"

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

func acquireLocalPurgeLock(ctx context.Context, base, id string) (func() error, error) {
	baseFD, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(baseFD) }()
	const lockDir = ".stella-home-purge-locks"
	if err := unix.Mkdirat(baseFD, lockDir, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, err
	}
	dirFD, err := unix.Openat(baseFD, lockDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(id)))
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = unix.Close(fd)
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() error { return unix.Close(fd) }, nil
}

func purgeLocalRelative(ctx context.Context, base, locator string) error {
	parts := splitLocalPath(locator)
	if len(parts) == 0 {
		return errors.New("empty purge locator")
	}
	parent := path.Join(parts[:len(parts)-1]...)
	if parent == "" {
		parent = "."
	}
	parentDir, err := openLocalDirNoFollow(base, parent)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer func() { _ = parentDir.Close() }()
	return purgeLocalAt(ctx, int(parentDir.Fd()), parts[len(parts)-1])
}

func purgeLocalAt(ctx context.Context, parentFD int, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		if err := unix.Unlinkat(parentFD, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		return nil
	}
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	entries, err := dir.ReadDir(-1)
	if err != nil {
		_ = dir.Close()
		return err
	}
	for _, entry := range entries {
		if err := purgeLocalAt(ctx, fd, entry.Name()); err != nil {
			_ = dir.Close()
			return err
		}
	}
	if err := dir.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return nil
}
