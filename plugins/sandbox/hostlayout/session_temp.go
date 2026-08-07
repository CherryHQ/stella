package hostlayout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CreateSessionTempDir creates a private physical backing directory outside
// the public sandbox coordinate roots and every authorized layout source.
// Keeping it in the user cache avoids the /tmp spelling collision: a host
// /tmp/stella-* path is otherwise indistinguishable from an agent-visible
// /tmp/stella-* path at string-only provider boundaries.
func CreateSessionTempDir(layout Layout, pattern string) (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache: %w", err)
	}
	return createSessionTempDir(layout, cache, pattern)
}

func createSessionTempDir(layout Layout, cache, pattern string) (string, error) {
	root := filepath.Join(cache, "stella", "sessions")
	if layoutOverlapsPath(layout, root) {
		return "", fmt.Errorf("session cache root %q overlaps an authorized layout source", root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create session cache root: %w", err)
	}
	return os.MkdirTemp(root, pattern)
}

func layoutOverlapsPath(layout Layout, candidate string) bool {
	candidate = resolvedCleanPath(candidate)
	for _, mount := range layout.Mounts {
		if pathsOverlap(resolvedCleanPath(mount.Source), candidate) {
			return true
		}
	}
	return false
}

func resolvedCleanPath(value string) string {
	value = filepath.Clean(value)
	for existing := value; ; existing = filepath.Dir(existing) {
		if resolved, err := filepath.EvalSymlinks(existing); err == nil {
			rel, relErr := filepath.Rel(existing, value)
			if relErr == nil {
				return filepath.Join(resolved, rel)
			}
			return filepath.Clean(resolved)
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return value
		}
	}
}

func pathsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
