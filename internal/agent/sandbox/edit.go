package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
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

func newEditTool(session pkgsandbox.FilesystemSession) pkgtools.Tool {
	return &hostEditTool{session: session}
}

type hostEditTool struct{ session pkgsandbox.FilesystemSession }

func (t *hostEditTool) Definition() pkgtools.Definition { return editDefinition() }
func (t *hostEditTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	input, oldStr, newStr := pkgtools.StringArg(args, "path"), pkgtools.StringArg(args, "old_string"), pkgtools.StringArg(args, "new_string")
	if input == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}
	p, err := resolveToolPath(t.session, input)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", input, err)
	}
	err = withFilesystem(t.session, func(filesystem pkgsandbox.Filesystem) error {
		raw, err := readAll(ctx, filesystem, p)
		if err != nil {
			return fmt.Errorf("read %s: %w", input, err)
		}
		fileContent := string(raw)
		count := strings.Count(fileContent, oldStr)
		if count == 0 {
			return fmt.Errorf("old_string not found in %s", input)
		}
		if count > 1 {
			return fmt.Errorf("old_string matches %d times in %s (must be unique)", count, input)
		}
		updated := strings.Replace(fileContent, oldStr, newStr, 1)
		length := int64(len(updated))
		return filesystem.Write(ctx, p, bytes.NewReader([]byte(updated)), pkgsandbox.WriteOptions{Perm: fs.FileMode(0o644), ContentLength: &length})
	})
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", input, err)
	}
	return fmt.Sprintf("Edited %s", input), nil
}
