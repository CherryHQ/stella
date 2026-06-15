package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnsureUserMiseHome creates the per-user mise tree at userToolsDir and seeds it
// with the shared system installs. Pointing a session's MISE_DATA_DIR at this
// tree gives the agent a real, writable mise home — it resolves the builtin
// tools (via the seeded symlinks) and can install its own tools/versions
// alongside, layered above the read-only system installs. Idempotent and safe to
// call before every session; a no-op when userToolsDir is empty.
//
// The tree is created 0700 (like the sibling per-user temp dir) so other host
// users can't read one tenant's installed tools, and every structural directory
// is verified to be a real directory — never a symlink. The tree is mounted
// writable into the sandbox and persists across sessions, so a prior session's
// agent could replace a structural dir with a symlink; since this runs unsandboxed
// with server privileges, following such a link would let seeding escape the tree.
//
// The "shims" dir is seeded empty on purpose: builtin tools resolve through the
// system shims (kept on PATH behind the per-user shims by HostEnvBuildPath), and
// the per-user shims fill in only as the agent installs its own tools.
func EnsureUserMiseHome(stellaHome, userToolsDir string) error {
	if userToolsDir == "" {
		return nil
	}
	if err := ensureRealDir(userToolsDir); err != nil {
		return err
	}
	for _, sub := range []string{"installs", "shims", "cache", "state", "config"} {
		if err := ensureRealDir(filepath.Join(userToolsDir, sub)); err != nil {
			return err
		}
	}
	return seedSystemInstalls(MiseToolsDir(stellaHome), userToolsDir)
}

// ensureRealDir creates dir (0700) if missing, or verifies an existing entry is a
// real directory. A symlink left in the writable, persisted per-user tree is
// removed (os.Remove targets the link, not what it points at) and replaced with a
// real dir, so later MkdirAll/Symlink can't write through it to escape the tree.
func ensureRealDir(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("sandbox: create per-user mise dir: %w", err)
		}
	case err != nil:
		return fmt.Errorf("sandbox: stat per-user mise dir: %w", err)
	case fi.Mode()&os.ModeSymlink != 0:
		if err := os.Remove(dir); err != nil {
			return fmt.Errorf("sandbox: remove symlinked per-user mise dir %q: %w", dir, err)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("sandbox: recreate per-user mise dir: %w", err)
		}
	case !fi.IsDir():
		return fmt.Errorf("sandbox: per-user mise path %q is not a directory", dir)
	}
	return nil
}

// seedSystemInstalls mirrors each system installs/<tool>/<version> into the
// per-user installs dir as a relative symlink. Relative targets resolve
// identically on the host and inside the sandbox — both trees are remapped under
// the same root — the same trick mise shims use (see relinkShims). Existing
// entries are left untouched, so a real user install of the same tool/version
// shadows the system one and re-seeding never clobbers user state.
func seedSystemInstalls(systemToolsDir, userToolsDir string) error {
	sysInstalls := filepath.Join(systemToolsDir, "installs")
	tools, err := os.ReadDir(sysInstalls)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sandbox: read system installs: %w", err)
	}
	userInstalls := filepath.Join(userToolsDir, "installs")
	for _, tool := range tools {
		if !tool.IsDir() {
			continue
		}
		versions, err := os.ReadDir(filepath.Join(sysInstalls, tool.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			// The shared system tree is mutated concurrently by the manifest/org
			// reconciler; a tool dir can vanish between the outer and inner read.
			// A transient race must not fail an unrelated user's session start.
			continue
		}
		if err != nil {
			return fmt.Errorf("sandbox: read system installs for %s: %w", tool.Name(), err)
		}
		userToolDir := filepath.Join(userInstalls, tool.Name())
		for _, v := range versions {
			link := filepath.Join(userToolDir, v.Name())
			if _, err := os.Lstat(link); err == nil {
				continue // already seeded, or shadowed by a real user install
			}
			if err := ensureRealDir(userToolDir); err != nil {
				return err
			}
			target, err := filepath.Rel(userToolDir, filepath.Join(sysInstalls, tool.Name(), v.Name()))
			if err != nil {
				return fmt.Errorf("sandbox: relative seed target: %w", err)
			}
			if err := os.Symlink(target, link); err != nil && !errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("sandbox: seed install symlink: %w", err)
			}
		}
	}
	return nil
}
