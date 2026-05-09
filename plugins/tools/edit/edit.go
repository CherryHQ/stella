package edit

import (
	"context"
	"fmt"
	"os"
	"strings"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

func init() {
	pkgplugins.Register("tool/edit", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:          "tool/edit",
			Kind:        "tool",
			Name:        "edit",
			DisplayName: "Edit",
			Description: "Apply exact string replacements to existing files.",
			Capabilities: []string{
				pkgplugins.CapabilityTool,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    "tool/edit",
			Name:        "edit",
			Description: "Edit existing files.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return NewEditTool(ctx.Paths.ProjectRoot), nil
			},
		})
	}))
}

// EditTool makes surgical edits to files by exact string replacement.
type EditTool struct {
	projectRoot string
}

func NewEditTool(projectRoot string) *EditTool {
	return &EditTool{projectRoot: projectRoot}
}

func (t *EditTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Path to the file to edit.",
				},
				"old_string": map[string]any{
					"type":        "string",
					"description": "The exact text to find and replace. Must match the file content exactly.",
				},
				"new_string": map[string]any{
					"type":        "string",
					"description": "The replacement text.",
				},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func (t *EditTool) Execute(_ context.Context, args map[string]any) (string, error) {
	requestedPath := tools.StringArg(args, "path")
	oldStr := tools.StringArg(args, "old_string")
	newStr := tools.StringArg(args, "new_string")

	if requestedPath == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}

	path, err := tools.ResolveProjectPath(t.projectRoot, requestedPath)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", requestedPath, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("edit: read %s: %w", requestedPath, err)
	}

	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("edit: old_string not found in %s", requestedPath)
	}
	if count > 1 {
		return "", fmt.Errorf("edit: old_string matches %d times in %s (must be unique)", count, requestedPath)
	}

	updated := strings.Replace(content, oldStr, newStr, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit: write %s: %w", requestedPath, err)
	}

	return fmt.Sprintf("Edited %s", requestedPath), nil
}
