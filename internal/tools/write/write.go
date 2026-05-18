package write

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/pkg/tools"
)

// WriteTool creates new files or completely overwrites existing ones.
type WriteTool struct {
	projectRoot string
}

func NewWriteTool(projectRoot string) *WriteTool {
	return &WriteTool{projectRoot: projectRoot}
}

func (t *WriteTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to create or overwrite.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full content to write to the file.",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *WriteTool) Execute(_ context.Context, args map[string]any) (string, error) {
	requestedPath := tools.StringArg(args, "path")
	content, _ := args["content"].(string)

	if requestedPath == "" {
		return "", fmt.Errorf("write: path is required")
	}

	path, err := tools.ResolveProjectPath(t.projectRoot, requestedPath)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", requestedPath, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir %s: %w", dir, err)
	}

	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	return fmt.Sprintf("Wrote %s (%d bytes)", requestedPath, len(content)), nil
}
