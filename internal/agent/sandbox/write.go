package sandbox

import (
	"context"
	"fmt"

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
				"path":    map[string]any{"type": "string", "description": "Relative paths are working/project files. Supported roots: $HOME, $STELLA_ASSETS_DIR, and $TMPDIR. Default work to $HOME; save final user deliverables in $STELLA_ASSETS_DIR when available."},
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

	resolvedPath, err := resolveToolExpression(t.host, t.projectRoot, path)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	if err := t.host.Files().WriteFile(resolvedPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", path, len(content)), nil
}
