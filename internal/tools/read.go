package coretools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func readDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":   map[string]any{"type": "string", "description": "Absolute or relative path to the file to read."},
				"offset": map[string]any{"type": "integer", "description": "Line number to start reading from (1-based). Defaults to 1."},
				"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to all lines."},
			},
			"required": []string{"path"},
		},
	}
}

func newReadTool(host sandbox.Host, projectRoot string) pkgtools.Tool {
	return &hostReadTool{host: host, projectRoot: projectRoot}
}

type hostReadTool struct {
	host        sandbox.Host
	projectRoot string
}

func (t *hostReadTool) Definition() pkgtools.Definition { return readDefinition() }

func (t *hostReadTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := pkgtools.StringArg(args, "path")
	if path == "" {
		return "", fmt.Errorf("read: path is required")
	}

	offset := max(toolIntArg(args, "offset", 1), 1)
	limit := toolIntArg(args, "limit", 0)

	resolvedPath, err := resolveToolPath(t.host, t.projectRoot, path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	// Binary detection on first page only.
	if offset <= 1 {
		sample := content
		if len(sample) > 8*1024 {
			sample = sample[:8*1024]
		}
		if len(sample) > 0 && pkgtools.IsBinary(string(sample)) {
			return "", fmt.Errorf("read %s: binary file detected — use bash with xxd, file, or other tools to inspect binary content", path)
		}
	}

	paged, totalLines := paginateReadContent(string(content), offset, limit)
	if totalLines == 0 {
		return "", nil
	}

	tr := pkgtools.TruncateHead(paged)
	linesConsumed := max(tr.OutputLines, 1)
	selectedLines := totalLines - (offset - 1)
	if limit > 0 {
		selectedLines = min(selectedLines, limit)
	}
	lastLineShown := offset + min(linesConsumed, selectedLines) - 1
	if lastLineShown < totalLines {
		tr.Content += fmt.Sprintf("\n[Use offset=%d to continue reading]", lastLineShown+1)
	}
	return tr.Content, nil
}

func paginateReadContent(content string, offset, limit int) (string, int) {
	lines := strings.SplitAfter(content, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	totalLines := len(lines)
	if totalLines == 0 || offset > totalLines {
		return "", totalLines
	}
	selected := lines[offset-1:]
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}
	return strings.Join(selected, ""), totalLines
}
