//go:build windows

package home

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

func openLocalDirNoFollow(base, locator string) (*os.File, error) {
	root, err := openLocalRootNoFollow(base, locator)
	if err != nil {
		return nil, err
	}
	file, err := root.Open(".")
	_ = root.Close()
	return file, err
}

func openLocalRootNoFollow(base, locator string) (*os.Root, error) {
	current, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	for _, part := range splitLocalPath(locator) {
		next, openErr := current.OpenRoot(part)
		info, lstatErr := current.Lstat(part)
		if openErr != nil || lstatErr != nil {
			_ = current.Close()
			if openErr != nil {
				return nil, openErr
			}
			return nil, lstatErr
		}
		openedInfo, statErr := next.Stat(".")
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			_ = current.Close()
			if statErr != nil {
				return nil, statErr
			}
			return nil, fmt.Errorf("path component %q is not a stable directory", part)
		}
		_ = current.Close()
		current = next
	}
	return current, nil
}

func prepareLocalWorkspaceScaffold(base, locator string, dirs []string) error {
	root, err := openLocalRootNoFollow(base, locator)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, dir := range dirs {
		if err := mkdirAllWindowsNoFollow(root, filepath.FromSlash(dir), 0o755); err != nil {
			return fmt.Errorf("create %q: %w", dir, err)
		}
	}
	return nil
}

func mkdirAllWindowsNoFollow(root *os.Root, name string, mode os.FileMode) error {
	current := root
	var opened []*os.Root
	defer func() {
		for _, dir := range opened {
			_ = dir.Close()
		}
	}()
	for _, part := range splitLocalPath(filepath.ToSlash(name)) {
		if err := current.Mkdir(part, mode); err != nil && !os.IsExist(err) {
			return err
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return err
		}
		info, err := current.Lstat(part)
		if err != nil {
			_ = next.Close()
			return err
		}
		openedInfo, err := next.Stat(".")
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, openedInfo) {
			_ = next.Close()
			if err != nil {
				return err
			}
			return fmt.Errorf("path component %q is not a stable directory", part)
		}
		opened = append(opened, next)
		current = next
	}
	return nil
}

func acquireLocalPurgeLock(ctx context.Context, base, id string) (func() error, error) {
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer baseRoot.Close()
	const lockDir = ".stella-home-purge-locks"
	if err := baseRoot.Mkdir(lockDir, 0o700); err != nil && !os.IsExist(err) {
		return nil, err
	}
	lockRoot, err := baseRoot.OpenRoot(lockDir)
	if err != nil {
		return nil, err
	}
	defer lockRoot.Close()
	name := fmt.Sprintf("%x.lock", sha256.Sum256([]byte(id)))
	file, err := lockRoot.OpenFile(name, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	h := windows.Handle(file.Fd())
	var ov windows.Overlapped
	for {
		err = windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
		if err == nil {
			break
		}
		if err != windows.ERROR_LOCK_VIOLATION {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return file.Close, nil
}

func purgeLocalRelative(ctx context.Context, base, locator string) error {
	parts := splitLocalPath(filepath.ToSlash(locator))
	if len(parts) == 0 {
		return fmt.Errorf("empty purge locator")
	}
	parent := filepath.Join(parts[:len(parts)-1]...)
	if parent == "" {
		parent = "."
	}
	root, err := openLocalRootNoFollow(base, filepath.ToSlash(parent))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer root.Close()
	return purgeWindowsAt(ctx, root, parts[len(parts)-1])
}

func purgeWindowsAt(ctx context.Context, parent *os.Root, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := parent.Lstat(name)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return parent.Remove(name)
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	openedInfo, err := child.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = child.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("purge path component %q was replaced", name)
	}
	dir, err := child.Open(".")
	if err != nil {
		_ = child.Close()
		return err
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		_ = child.Close()
		return err
	}
	for _, entry := range entries {
		if err := purgeWindowsAt(ctx, child, entry.Name()); err != nil {
			_ = child.Close()
			return err
		}
	}
	if err := child.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := parent.Remove(name); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
