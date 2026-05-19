package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProjectResolverFunc resolves a project ID to its base directory path.
// Returns the absolute base_dir for the project.
type ProjectResolverFunc func(ctx context.Context, projectID, userID string) (baseDir string, err error)

// ValidateProjectDir checks that baseDir is a safe subpath of userRoot.
// It rejects paths containing ".." traversal or paths outside the workspace.
func ValidateProjectDir(baseDir, userRoot string) error {
	if baseDir == "" {
		return fmt.Errorf("base_dir must not be empty")
	}
	if !filepath.IsAbs(baseDir) {
		return fmt.Errorf("base_dir must be absolute")
	}
	if !filepath.IsAbs(userRoot) {
		return fmt.Errorf("user_root must be absolute")
	}

	cleanBase := filepath.Clean(baseDir)
	cleanRoot := filepath.Clean(userRoot)

	// Resolve symlinks when paths exist on disk.
	if resolved, err := filepath.EvalSymlinks(cleanRoot); err == nil {
		cleanRoot = resolved
	}
	if resolved, err := filepath.EvalSymlinks(cleanBase); err == nil {
		cleanBase = resolved
	} else {
		// Path doesn't exist yet — resolve the longest existing prefix
		// to catch symlink escapes in parent directories.
		dir := cleanBase
		for dir != "/" && dir != "." {
			parent := filepath.Dir(dir)
			if resolved, err := filepath.EvalSymlinks(parent); err == nil {
				cleanBase = filepath.Join(resolved, cleanBase[len(parent):])
				break
			}
			dir = parent
		}
	}

	// Equal is OK (root project uses UserRoot as base_dir).
	if cleanBase == cleanRoot {
		return nil
	}

	rel, err := filepath.Rel(cleanRoot, cleanBase)
	if err != nil {
		return fmt.Errorf("base_dir is not under user workspace")
	}
	if strings.HasPrefix(rel, "..") {
		return fmt.Errorf("base_dir must be under user workspace")
	}

	return nil
}

// EnsureProjectDir creates the project directory if it doesn't exist.
func EnsureProjectDir(baseDir string) error {
	return os.MkdirAll(baseDir, 0o755)
}
