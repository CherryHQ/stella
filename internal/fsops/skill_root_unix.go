//go:build darwin || linux

package fsops

import (
	"errors"
	"fmt"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/CherryHQ/stella/pkg/sandbox"
)

// NewSkillFilesystem opens one exact, writable catalog subroot beneath a
// configured Store base. It rejects symlinks in every relative component and
// never reopens the relative path after verifying it. Generic NewFilesystem
// deliberately retains ordinary contained-POSIX-symlink semantics instead.
func NewSkillFilesystem(base, relative string) (*Filesystem, error) {
	return newSkillFilesystem(base, relative, unix.Fsync)
}

// newSkillFilesystem accepts a per-call durability seam so tests can fail a
// newly-created parent sync without mutable package-global state.
func newSkillFilesystem(base, relative string, syncParent func(int) error) (*Filesystem, error) {
	if base == "" || syncParent == nil || !cleanRelativeDirectory(relative) {
		return nil, errors.New("fsops: invalid Skill filesystem root")
	}
	fd, err := openNoFollowDirectoryTree(base, relative, syncParent)
	if err != nil {
		return nil, err
	}
	pinned := os.NewFile(uintptr(fd), "skill-filesystem-root")
	bridge := skillRootFDPath(fd)
	root, err := os.OpenRoot(bridge)
	if err != nil {
		_ = pinned.Close()
		return nil, fmt.Errorf("fsops: open pinned Skill root: %w", err)
	}
	return &Filesystem{mounts: []mountedRoot{{
		path: sandbox.PathWorkspace,
		root: &Root{root: root, pinned: pinned},
	}}}, nil
}

func cleanRelativeDirectory(relative string) bool {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.Contains(relative, `\`) {
		return false
	}
	for part := range strings.SplitSeq(relative, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func openNoFollowDirectoryTree(base, relative string, syncParent func(int) error) (int, error) {
	fd, err := unix.Open(base, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("fsops: open Skill Store base: %w", err)
	}
	for component := range strings.SplitSeq(relative, "/") {
		next, err := openOrMakeNoFollowDirectory(fd, component)
		if err != nil {
			_ = unix.Close(fd)
			return -1, fmt.Errorf("fsops: open Skill root component %q: %w", component, err)
		}
		// This also persists locator ancestors that LocalStore.Ensure created
		// before this strict walk. Newly-created components reach this point
		// only after their no-follow open succeeds, so every parent is synced
		// exactly once before advancing.
		if err := syncParent(fd); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(fd)
			return -1, fmt.Errorf("fsops: sync Skill root parent: %w", err)
		}
		if err := unix.Close(fd); err != nil {
			_ = unix.Close(next)
			return -1, fmt.Errorf("fsops: close Skill root parent: %w", err)
		}
		fd = next
	}
	return fd, nil
}

func openOrMakeNoFollowDirectory(parent int, name string) (int, error) {
	for attempt := 0; attempt != 2; attempt++ {
		fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == nil {
			return fd, nil
		}
		if !errors.Is(err, unix.ENOENT) {
			return -1, err
		}
		if err := unix.Mkdirat(parent, name, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	return -1, errors.New("directory changed while creating Skill root")
}

func skillRootFDPath(fd int) string {
	if runtime.GOOS == "darwin" {
		return "/dev/fd/" + strconv.Itoa(fd)
	}
	return "/proc/self/fd/" + strconv.Itoa(fd)
}
