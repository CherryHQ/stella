package sandbox

import (
	"context"
	"fmt"
	"strings"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func editDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "Relative paths are working/project files. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR. Default work to $HOME; save final user deliverables in $STELLA_ASSETS_DIR when available."},
				"old_string": map[string]any{"type": "string", "description": "The exact text to find and replace. Must match the file content exactly."},
				"new_string": map[string]any{"type": "string", "description": "The replacement text."},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func newEditTool(host pkgsandbox.Session) pkgtools.Tool {
	return &hostEditTool{host: host}
}

type hostEditTool struct {
	host pkgsandbox.Session
}

func (t *hostEditTool) Definition() pkgtools.Definition { return editDefinition() }

func (t *hostEditTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path := pkgtools.StringArg(args, "path")
	oldStr := pkgtools.StringArg(args, "old_string")
	newStr := pkgtools.StringArg(args, "new_string")
	if path == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}

	view, err := pkgsandbox.SelectFileView(ctx, t.host)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	resolvedPath, err := resolveToolExpression(view.Policy.Env, view.WorkingDir, path)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}

	raw, err := view.Files.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("edit: read %s: %w", path, err)
	}
	fileContent := string(raw)
	count := strings.Count(fileContent, oldStr)
	if count == 0 {
		return "", fmt.Errorf("edit: old_string not found in %s", path)
	}
	if count > 1 {
		return "", fmt.Errorf("edit: old_string matches %d times in %s (must be unique)", count, path)
	}

	updated := strings.Replace(fileContent, oldStr, newStr, 1)
	if err := view.Files.WriteFile(resolvedPath, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	return fmt.Sprintf("Edited %s", path), nil
}
