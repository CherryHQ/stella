//go:build unix

package home

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func openWorkspaceRoot(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("home: pin STELLA_HOME: %w", err)
	}
	return fd, nil
}

func closeWorkspaceRoot(fd int) error { return unix.Close(fd) }

func (m *WorkspaceManager) verifyPinnedRoot() error {
	var pinned, current unix.Stat_t
	if err := unix.Fstat(m.rootFD, &pinned); err != nil {
		return fmt.Errorf("home: inspect pinned STELLA_HOME: %w", err)
	}
	if err := unix.Fstatat(unix.AT_FDCWD, m.base, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("home: inspect current STELLA_HOME: %w", err)
	}
	if pinned.Dev != current.Dev || pinned.Ino != current.Ino || current.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errors.New("home: STELLA_HOME was replaced")
	}
	return nil
}

func (m *WorkspaceManager) ensureChain(parts ...string) error {
	if err := m.verifyPinnedRoot(); err != nil {
		return err
	}
	fd, err := unix.Dup(m.rootFD)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, part := range parts {
		if err := validID(part); err != nil {
			return err
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openErr, syscall.ENOENT) {
			if err := unix.Mkdirat(fd, part, 0o755); err != nil && !errors.Is(err, syscall.EEXIST) {
				return fmt.Errorf("home: create typed root: %w", err)
			}
			next, openErr = unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			return fmt.Errorf("home: open typed root %q: %w", part, openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func (m *WorkspaceManager) agentIDOccupied(id string) (bool, error) {
	if err := m.verifyPinnedRoot(); err != nil {
		return true, err
	}
	agents, err := unix.Openat(m.rootFD, "agents", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	if err != nil {
		return true, fmt.Errorf("home: inspect agents root: %w", err)
	}
	defer func() { _ = unix.Close(agents) }()
	var st unix.Stat_t
	err = unix.Fstatat(agents, id, &st, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.ENOENT) {
		return false, nil
	}
	return true, err
}
