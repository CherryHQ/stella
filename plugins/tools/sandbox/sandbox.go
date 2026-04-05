package sandbox

import (
	"context"

	"github.com/vaayne/anna/internal/auth"
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
			if err := auth.ValidatePath(s.allowedDir, path); err != nil {
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
