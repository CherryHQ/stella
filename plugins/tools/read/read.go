package read

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
	"github.com/vaayne/anna/plugins/tools/sandbox"
)

func init() {
	pkgplugins.Register("tool/read", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:          "tool/read",
			Kind:        "tool",
			Name:        "read",
			DisplayName: "Read",
			Description: "Read file contents with pagination and truncation.",
			Capabilities: []string{
				pkgplugins.CapabilityTool,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    "tool/read",
			Name:        "read",
			Description: "Read file contents.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return sandbox.WrapWithSandbox(&ReadTool{}, ctx.UserDataDir, "file_path"), nil
			},
		})
	}))
}

// ReadTool reads file contents.
type ReadTool struct{}

func (t *ReadTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file to read.",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Line number to start reading from (1-based). Defaults to 1.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to read. Defaults to all lines.",
				},
			},
			"required": []string{"file_path"},
		},
	}
}

func (t *ReadTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path, ok := args["file_path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("read: file_path is required")
	}

	offset := intArg(args, "offset", 1)
	limit := intArg(args, "limit", 0)
	if offset < 1 {
		offset = 1
	}

	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	// Binary guard: sample the first bytes to detect non-text content.
	if offset <= 1 {
		sample := make([]byte, 8*1024)
		n, _ := f.Read(sample)
		if n > 0 && tools.IsBinary(string(sample[:n])) {
			return "", fmt.Errorf("read %s: binary file detected — use bash with xxd, file, or other tools to inspect binary content", path)
		}
		// Reset to beginning for normal reading.
		if _, err := f.Seek(0, 0); err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	}

	lines, totalLines, err := scanLines(f, offset, limit)
	if err != nil {
		// Fall back to reading the whole file if scanner fails
		// (e.g., lines longer than scanner buffer).
		_ = f.Close()
		return t.readFallback(path, offset, limit)
	}

	// bufio.Scanner strips newlines; we added them back in scanLines.
	// If we collected the last line and file doesn't end with \n, trim it.
	if len(lines) > 0 && totalLines == offset+len(lines)-1 {
		if !endsWithNewline(f) {
			lines[len(lines)-1] = strings.TrimSuffix(lines[len(lines)-1], "\n")
		}
	}

	content := strings.Join(lines, "")
	tr := tools.TruncateHead(content)

	// Ensure pagination advances by at least 1 line to avoid infinite loops
	// (e.g., when a single line exceeds the byte limit and OutputLines == 0).
	linesConsumed := max(tr.OutputLines, 1)
	lastLineShown := offset + linesConsumed - 1
	if lastLineShown < totalLines {
		hint := fmt.Sprintf("\n[Use offset=%d to continue reading]", lastLineShown+1)
		tr.Content += hint
	}

	return tr.Content, nil
}

// scanLines streams through the file, skipping to offset and collecting up to limit lines.
func scanLines(f *os.File, offset, limit int) (lines []string, totalLines int, err error) {
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		totalLines++
		if totalLines < offset {
			continue
		}
		if limit > 0 && len(lines) >= limit {
			continue
		}
		lines = append(lines, scanner.Text()+"\n")
	}
	return lines, totalLines, scanner.Err()
}

// readFallback reads the file using os.ReadFile when the scanner fails (e.g., lines > 1MB).
func (t *ReadTool) readFallback(path string, offset, limit int) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	allLines := tools.SplitLines(string(data))
	totalLines := len(allLines)

	start := min(offset-1, totalLines)
	selected := allLines[start:]

	if limit > 0 && limit < len(selected) {
		selected = selected[:limit]
	}

	content := strings.Join(selected, "")
	tr := tools.TruncateHead(content)

	linesConsumed := max(tr.OutputLines, 1)
	lastLineShown := offset + linesConsumed - 1
	if lastLineShown < totalLines {
		hint := fmt.Sprintf("\n[Use offset=%d to continue reading]", lastLineShown+1)
		tr.Content += hint
	}

	return tr.Content, nil
}

// endsWithNewline checks whether the already-open file ends with a newline.
func endsWithNewline(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return false
	}
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, fi.Size()-1); err != nil {
		return false
	}
	return buf[0] == '\n'
}

func intArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return defaultVal
	}
}
