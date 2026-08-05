package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"path"

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

func newWriteTool(session pkgsandbox.FilesystemSession) pkgtools.Tool {
	return &hostWriteTool{session: session}
}

type hostWriteTool struct{ session pkgsandbox.FilesystemSession }

func (t *hostWriteTool) Definition() pkgtools.Definition { return writeDefinition() }
func (t *hostWriteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	input := pkgtools.StringArg(args, "path")
	content, _ := args["content"].(string)
	if input == "" {
		return "", fmt.Errorf("write: path is required")
	}
	p, err := resolveToolPath(t.session, input)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", input, err)
	}
	length := int64(len(content))
	err = withFilesystem(t.session, func(filesystem pkgsandbox.Filesystem) error {
		if err := filesystem.Mkdir(ctx, path.Dir(p), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", path.Dir(p), err)
		}
		return filesystem.Write(ctx, p, bytes.NewReader([]byte(content)), pkgsandbox.WriteOptions{Perm: fs.FileMode(0o644), ContentLength: &length})
	})
	if err != nil {
		return "", fmt.Errorf("write %s: %w", input, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", input, len(content)), nil
}
