package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func writeDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file to create or overwrite."},
				"content": map[string]any{"type": "string", "description": "The full content to write to the file."},
			},
			"required": []string{"path", "content"},
		},
	}
}

func newWriteTool(host pkgsandbox.Host, projectRoot string) pkgtools.Tool {
	return &hostWriteTool{host: host, projectRoot: projectRoot}
}

type hostWriteTool struct {
	host        pkgsandbox.Host
	projectRoot string
}

func (t *hostWriteTool) Definition() pkgtools.Definition { return writeDefinition() }

func (t *hostWriteTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := pkgtools.StringArg(args, "path")
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("write: path is required")
	}

	resolvedPath, err := resolveWritableToolPath(t.host, t.projectRoot, path)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir %s: %w", filepath.Dir(resolvedPath), err)
	}

	if err := os.WriteFile(resolvedPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", path, len(content)), nil
}
