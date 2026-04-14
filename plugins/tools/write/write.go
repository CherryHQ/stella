package write

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

func init() {
	pkgplugins.Register("tool/write", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:          "tool/write",
			Kind:        "tool",
			Name:        "write",
			DisplayName: "Write",
			Description: "Create or fully overwrite files.",
			Capabilities: []string{
				pkgplugins.CapabilityTool,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    "tool/write",
			Name:        "write",
			Description: "Write complete file contents.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return NewWriteTool(ctx.WorkDir), nil
			},
		})
	}))
}

// WriteTool creates new files or completely overwrites existing ones.
type WriteTool struct {
	workDir string
}

func NewWriteTool(workDir string) *WriteTool {
	return &WriteTool{workDir: workDir}
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
				"file_path": map[string]any{
					"type":        "string",
					"description": "Legacy alias for path.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full content to write to the file.",
				},
			},
			"required": []string{"content"},
			"anyOf": []map[string]any{
				{"required": []string{"path"}},
				{"required": []string{"file_path"}},
			},
		},
	}
}

func (t *WriteTool) Execute(_ context.Context, args map[string]any) (string, error) {
	requestedPath := tools.StringArg(args, "path", "file_path")
	content, _ := args["content"].(string)

	if requestedPath == "" {
		return "", fmt.Errorf("write: path is required")
	}

	path, err := tools.ResolvePath(t.workDir, requestedPath)
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
