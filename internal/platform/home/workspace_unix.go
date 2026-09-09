//go:build unix

package home

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const maintenanceListLimit = 10001

type existingSkillRoot struct {
	request WorkspaceRequest
	scope   RootScope
}

var fsyncWorkspaceFD = unix.Fsync

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

// syncChain fences the complete typed-root ancestry using only descriptors
// beneath the pinned STELLA_HOME. It deliberately re-fsyncs visible components
// so a crash after mkdir but before its parent fence is repaired on resume.
func (m *WorkspaceManager) syncChain(parts ...string) error {
	if err := m.verifyPinnedRoot(); err != nil {
		return err
	}
	fd, err := unix.Dup(m.rootFD)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	if err := fsyncWorkspaceFD(fd); err != nil {
		return err
	}
	for _, part := range parts {
		if err := validID(part); err != nil {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("home: open typed root %q for sync: %w", part, err)
		}
		if err := fsyncWorkspaceFD(fd); err != nil {
			_ = unix.Close(next)
			return err
		}
		if err := fsyncWorkspaceFD(next); err != nil {
			_ = unix.Close(next)
			return err
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func (m *WorkspaceManager) openOperationsRoot(parts ...string) (*os.Root, error) {
	if err := m.verifyPinnedRoot(); err != nil {
		return nil, err
	}
	fd, err := unix.Dup(m.rootFD)
	if err != nil {
		return nil, err
	}
	defer func() {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
	}()
	for _, part := range parts {
		if err := validID(part); err != nil {
			return nil, err
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return nil, fmt.Errorf("home: securely open typed root: %w", openErr)
		}
		_ = unix.Close(fd)
		fd = next
	}
	pinned := os.NewFile(uintptr(fd), "stella-workspace-root")
	if pinned == nil {
		return nil, errors.New("home: wrap typed root descriptor")
	}
	fd = -1 // pinned now owns the descriptor.
	defer func() { _ = pinned.Close() }()
	expected, err := pinned.Stat()
	if err != nil {
		return nil, fmt.Errorf("home: inspect typed root descriptor: %w", err)
	}
	rootPath := filepath.Join(append([]string{m.base}, parts...)...)
	r, err := os.OpenRoot(rootPath)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return nil, fmt.Errorf("home: open operation root: %w", err)
	}
	actual, err := r.Stat(".")
	if err != nil {
		_ = r.Close()
		return nil, fmt.Errorf("home: inspect operation root: %w", err)
	}
	if !os.SameFile(expected, actual) {
		_ = r.Close()
		return nil, errors.New("home: operation root inode mismatch")
	}
	return r, nil
}

// WalkExistingSkillRoots is intentionally separate from the normal opener:
// cleanup may need to inspect roots whose database owner was already deleted.
// It enumerates first, closes the inventory root, and then opens one mutable
// typed root at a time so the 257-bucket owner locks cannot self-deadlock.
func (m *WorkspaceManager) WalkExistingSkillRoots(ctx context.Context, visit func(WorkspaceRequest, RootScope, SkillRootOperations) error) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if visit == nil {
		return errors.New("home: Skill root maintenance callback is required")
	}
	inventory, err := m.openOperationsRoot()
	if err != nil {
		return err
	}
	candidates, scanErr := existingSkillRoots(inventory)
	closeErr := inventory.Close()
	if scanErr != nil || closeErr != nil {
		return errors.Join(scanErr, closeErr)
	}
	for _, candidate := range candidates {
		if err := checkContext(ctx); err != nil {
			return err
		}
		root, err := m.openRoot(ctx, candidate.request, candidate.scope, RootReadWrite, false, false)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		visitErr := visit(candidate.request, candidate.scope, root)
		closeErr := root.Close()
		if visitErr != nil || closeErr != nil {
			return errors.Join(visitErr, closeErr)
		}
	}
	return nil
}

func existingSkillRoots(root *os.Root) ([]existingSkillRoot, error) {
	var candidates []existingSkillRoot
	entriesSeen := 0
	addCandidate := func(candidate existingSkillRoot) error {
		if len(candidates) >= maintenanceListLimit {
			return ErrListLimit
		}
		candidates = append(candidates, candidate)
		return nil
	}
	if ok, err := existingDirectory(root, ".agents/db-skills"); err != nil {
		return nil, err
	} else if ok {
		if err := addCandidate(existingSkillRoot{scope: RootSystemSkills}); err != nil {
			return nil, err
		}
	}
	agents, err := listExistingChildren(root, "agents")
	if err != nil {
		return nil, err
	}
	for _, entry := range agents {
		entriesSeen++
		if entriesSeen > maintenanceListLimit {
			return nil, ErrListLimit
		}
		if err := validID(entry.Name()); err != nil {
			continue
		}
		path := filepath.Join("agents", entry.Name(), ".agents", "skills")
		if ok, err := existingDirectory(root, path); err != nil {
			return nil, err
		} else if ok {
			if err := addCandidate(existingSkillRoot{
				request: WorkspaceRequest{AgentID: entry.Name()}, scope: RootSystemAgentSkills,
			}); err != nil {
				return nil, err
			}
		}
	}
	users, err := listExistingChildren(root, "users")
	if err != nil {
		return nil, err
	}
	for _, entry := range users {
		entriesSeen++
		if entriesSeen > maintenanceListLimit {
			return nil, ErrListLimit
		}
		if err := validID(entry.Name()); err != nil {
			continue
		}
		userID := entry.Name()
		userSkillPath := filepath.Join("users", userID, ".agents", "skills")
		if ok, err := existingDirectory(root, userSkillPath); err != nil {
			return nil, err
		} else if ok {
			if err := addCandidate(existingSkillRoot{
				request: WorkspaceRequest{UserID: userID}, scope: RootUserSkills,
			}); err != nil {
				return nil, err
			}
		}
		agentSkillsPath := filepath.Join("users", userID, ".agents", "agent-skills")
		agentSkills, err := listExistingChildren(root, agentSkillsPath)
		if err != nil {
			return nil, err
		}
		for _, agent := range agentSkills {
			entriesSeen++
			if entriesSeen > maintenanceListLimit {
				return nil, ErrListLimit
			}
			if err := validID(agent.Name()); err != nil {
				continue
			}
			if err := addCandidate(existingSkillRoot{
				request: WorkspaceRequest{UserID: userID, AgentID: agent.Name()}, scope: RootUserAgentSkills,
			}); err != nil {
				return nil, err
			}
		}
	}
	return candidates, nil
}

func listExistingChildren(root *os.Root, name string) ([]os.DirEntry, error) {
	ok, err := existingDirectory(root, name)
	if errors.Is(err, fs.ErrNotExist) || !ok && err == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	file, err := root.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	entries, err := file.ReadDir(maintenanceListLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maintenanceListLimit {
		return nil, ErrListLimit
	}
	return entries, nil
}

func existingDirectory(root *os.Root, name string) (bool, error) {
	current := "."
	for component := range strings.SplitSeq(filepath.ToSlash(name), "/") {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := root.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
			return false, fmt.Errorf("home: maintenance root component %q is not a real directory", current)
		}
	}
	return true, nil
}

func openRootFile(root *os.Root, name string, flag int, perm os.FileMode) (*os.File, error) {
	return root.OpenFile(name, flag|unix.O_NONBLOCK, perm)
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
