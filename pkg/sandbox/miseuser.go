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
func EnsureUserMiseHome(stellaHome, userToolsDir string) error {
	if userToolsDir == "" {
		return nil
	}
	for _, sub := range []string{"installs", "shims", "cache", "state", "config"} {
		if err := os.MkdirAll(filepath.Join(userToolsDir, sub), 0o755); err != nil {
			return fmt.Errorf("sandbox: create per-user mise dir: %w", err)
		}
	}
	return seedSystemInstalls(MiseToolsDir(stellaHome), userToolsDir)
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
		if err != nil {
			return fmt.Errorf("sandbox: read system installs for %s: %w", tool.Name(), err)
		}
		userToolDir := filepath.Join(userInstalls, tool.Name())
		for _, v := range versions {
			link := filepath.Join(userToolDir, v.Name())
			if _, err := os.Lstat(link); err == nil {
				continue // already seeded, or shadowed by a real user install
			}
			if err := os.MkdirAll(userToolDir, 0o755); err != nil {
				return fmt.Errorf("sandbox: create per-user tool dir: %w", err)
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
