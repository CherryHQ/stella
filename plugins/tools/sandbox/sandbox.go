package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vaayne/anna/pkg/tools"
)

// closeableTool is an optional interface for tools that need cleanup.
type closeableTool interface {
	Close() error
}

// sandboxTool wraps a Tool with path validation. It checks that the value of
// the specified argument key is within the allowed directory before delegating
// to the underlying tool. This is a defense-in-depth measure.
type sandboxTool struct {
	inner      tools.Tool
	allowedDir string
	pathKey    string // argument key containing the file path (e.g. "file_path")
}

func (s *sandboxTool) Definition() tools.Definition {
	return s.inner.Definition()
}

func (s *sandboxTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if s.allowedDir != "" {
		if path, ok := args[s.pathKey].(string); ok && path != "" {
			if err := validatePath(s.allowedDir, path); err != nil {
				return "", err
			}
		}
	}
	return s.inner.Execute(ctx, args)
}

func (s *sandboxTool) Close() error {
	if c, ok := s.inner.(closeableTool); ok {
		return c.Close()
	}
	return nil
}

// WrapWithSandbox returns a sandbox-wrapped tool if allowedDir is non-empty.
// Otherwise it returns the original tool unchanged.
func WrapWithSandbox(t tools.Tool, allowedDir, pathKey string) tools.Tool {
	if allowedDir == "" {
		return t
	}
	return &sandboxTool{
		inner:      t,
		allowedDir: allowedDir,
		pathKey:    pathKey,
	}
}

// validatePath checks that requestedPath is within allowedDir after resolving
// symlinks. This is a defense-in-depth measure to prevent accidental
// cross-user file access — not a hard security boundary.
//
// Copied from internal/auth/sandbox.go to avoid an internal/ dependency.
func validatePath(allowedDir, requestedPath string) error {
	if allowedDir == "" {
		return nil // no sandbox configured
	}

	// Resolve the allowed directory (it should already exist).
	resolvedAllowed, err := filepath.EvalSymlinks(allowedDir)
	if err != nil {
		return fmt.Errorf("sandbox: resolve allowed dir: %w", err)
	}
	resolvedAllowed = filepath.Clean(resolvedAllowed)

	// For the requested path, try resolving symlinks. If the file doesn't
	// exist yet (e.g. write/create), resolve the deepest existing parent.
	resolvedPath, err := resolvePathBestEffort(requestedPath)
	if err != nil {
		return fmt.Errorf("sandbox: resolve path: %w", err)
	}

	// Ensure the resolved path is within the allowed directory.
	if !strings.HasPrefix(resolvedPath, resolvedAllowed+string(os.PathSeparator)) && resolvedPath != resolvedAllowed {
		return fmt.Errorf("sandbox: path %q is outside allowed directory %q", requestedPath, allowedDir)
	}

	return nil
}

// resolvePathBestEffort resolves symlinks for the given path. If the path does
// not exist, it walks up to the nearest existing ancestor, resolves it, and
// appends the remaining segments.
func resolvePathBestEffort(path string) (string, error) {
	cleaned := filepath.Clean(path)

	// Try full path first.
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return filepath.Clean(resolved), nil
	}

	// Walk up to find the nearest existing ancestor.
	remaining := ""
	current := cleaned
	for {
		parent := filepath.Dir(current)
		if parent == current {
			// Reached root without finding an existing path.
			return filepath.Clean(path), nil
		}
		remaining = filepath.Join(filepath.Base(current), remaining)
		current = parent

		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Clean(filepath.Join(resolved, remaining)), nil
		}
	}
}
