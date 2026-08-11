//go:build windows

package home

import (
	"fmt"
	"os"
	"path/filepath"
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
