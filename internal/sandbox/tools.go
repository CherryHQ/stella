package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/vaayne/anna/pkg/tools"
)

// NewCoreTools returns the unified host-backed core tools.
func NewCoreTools(host Host, toolsBinDir string) []tools.Tool {
	if host == nil {
		return nil
	}
	return []tools.Tool{
		newBashTool(host, toolsBinDir),
		newReadTool(host),
		newWriteTool(host),
		newEditTool(host),
	}
}

func newBashTool(host Host, toolsBinDir string) tools.Tool {
	return &hostBashTool{host: host, normalizer: newToolNormalizer(), toolsBinDir: toolsBinDir}
}

func newReadTool(host Host) tools.Tool  { return &hostReadTool{host: host} }
func newWriteTool(host Host) tools.Tool { return &hostWriteTool{host: host} }
func newEditTool(host Host) tools.Tool  { return &hostEditTool{host: host} }

type hostBashTool struct {
	host        Host
	normalizer  *toolNormalizer
	toolsBinDir string
}

func (t *hostBashTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "bash",
		Description: "Execute a bash command. Use for file operations like ls, rg, find, git, and other shell commands.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The bash command to execute."},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds."},
			},
			"required": []string{"command"},
		},
	}
}

func (t *hostBashTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	start := time.Now()
	env := map[string]string{}
	if t.toolsBinDir != "" {
		env["PATH"] = t.toolsBinDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	result, err := t.host.Exec(ctx, command, ExecOptions{Timeout: time.Duration(toolIntArg(args, "timeout", 0)) * time.Second, Env: env})
	if err != nil {
		norm := t.normalizer.NormalizeError(err, "bash")
		return norm.Content, fmt.Errorf("bash: %w", err)
	}

	norm := t.normalizer.NormalizeExec(result, time.Since(start))
	if norm.IsError {
		return norm.Content, fmt.Errorf("bash: exit code %d", result.ExitCode)
	}
	return norm.Content, nil
}

type LineOrientedReaderHost interface {
	Host
	ReadFileLines(ctx context.Context, path string, offset, limit int) (ReadResult, error)
	ReadAllFile(ctx context.Context, path string) ([]byte, error)
}

type hostReadTool struct{ host Host }

func (t *hostReadTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "Absolute or relative path to the file to read."},
				"file_path": map[string]any{"type": "string", "description": "Legacy alias for path."},
				"offset":    map[string]any{"type": "integer", "description": "Line number to start reading from (1-based). Defaults to 1."},
				"limit":     map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to all lines."},
			},
			"anyOf": []map[string]any{
				{"required": []string{"path"}},
				{"required": []string{"file_path"}},
			},
		},
	}
}

func (t *hostReadTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path := tools.StringArg(args, "path", "file_path")
	if path == "" {
		return "", fmt.Errorf("read: path is required")
	}

	offset := max(toolIntArg(args, "offset", 1), 1)
	limit := toolIntArg(args, "limit", 0)

	if reader, ok := t.host.(LineOrientedReaderHost); ok {
		return t.executeLineReader(ctx, reader, path, offset, limit)
	}

	result, err := t.host.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	if offset <= 1 {
		sample := result.Content
		if len(sample) > 8*1024 {
			sample = sample[:8*1024]
		}
		if len(sample) > 0 && tools.IsBinary(string(sample)) {
			return "", fmt.Errorf("read %s: binary file detected — use bash with xxd, file, or other tools to inspect binary content", path)
		}
	}

	content, totalLines := paginateReadContent(string(result.Content), offset, limit)
	if totalLines == 0 {
		return "", nil
	}

	tr := tools.TruncateHead(content)
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

type hostWriteTool struct{ host Host }

func (t *hostWriteTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":      map[string]any{"type": "string", "description": "Path to the file to create or overwrite."},
				"file_path": map[string]any{"type": "string", "description": "Legacy alias for path."},
				"content":   map[string]any{"type": "string", "description": "The full content to write to the file."},
			},
			"required": []string{"content"},
			"anyOf": []map[string]any{
				{"required": []string{"path"}},
				{"required": []string{"file_path"}},
			},
		},
	}
}

func (t *hostWriteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path := tools.StringArg(args, "path", "file_path")
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("write: path is required")
	}

	if err := t.host.MkdirAll(ctx, filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir %s: %w", filepath.Dir(path), err)
	}

	result, err := t.host.WriteFile(ctx, path, []byte(content))
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", path, result.BytesWritten), nil
}

type hostEditTool struct{ host Host }

func (t *hostEditTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "Path to the file to edit."},
				"file_path":  map[string]any{"type": "string", "description": "Legacy alias for path."},
				"oldText":    map[string]any{"type": "string", "description": "Preferred alias for old_string."},
				"old_string": map[string]any{"type": "string", "description": "The exact text to find and replace. Must match the file content exactly."},
				"newText":    map[string]any{"type": "string", "description": "Preferred alias for new_string."},
				"new_string": map[string]any{"type": "string", "description": "The replacement text."},
			},
			"anyOf": []map[string]any{
				{"required": []string{"path", "oldText", "newText"}},
				{"required": []string{"path", "old_string", "new_string"}},
				{"required": []string{"file_path", "oldText", "newText"}},
				{"required": []string{"file_path", "old_string", "new_string"}},
			},
		},
	}
}

func (t *hostEditTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path := tools.StringArg(args, "path", "file_path")
	oldStr := tools.StringArg(args, "oldText", "old_string")
	newStr := tools.StringArg(args, "newText", "new_string")
	if path == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: oldText is required")
	}

	content, err := readAllForEdit(ctx, t.host, path)
	if err != nil {
		return "", fmt.Errorf("edit: read %s: %w", path, err)
	}
	count := strings.Count(string(content), oldStr)
	if count == 0 {
		return "", fmt.Errorf("edit: oldText not found in %s", path)
	}
	if count > 1 {
		return "", fmt.Errorf("edit: oldText matches %d times in %s (must be unique)", count, path)
	}

	result, err := t.host.EditFile(ctx, path, []Edit{{OldText: oldStr, NewText: newStr}})
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	if result.AppliedEdits == 1 {
		return fmt.Sprintf("Edited %s", path), nil
	}
	return fmt.Sprintf("Edited %s (%d replacements)", path, result.AppliedEdits), nil
}

func (t *hostReadTool) executeLineReader(ctx context.Context, host LineOrientedReaderHost, path string, offset, limit int) (string, error) {
	result, err := host.ReadFileLines(ctx, path, offset, limit)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if offset <= 1 {
		sample := result.Content
		if len(sample) > 8*1024 {
			sample = sample[:8*1024]
		}
		if len(sample) > 0 && tools.IsBinary(string(sample)) {
			return "", fmt.Errorf("read %s: binary file detected — use bash with xxd, file, or other tools to inspect binary content", path)
		}
	}
	if len(result.Content) == 0 {
		return "", nil
	}
	tr := tools.TruncateHead(string(result.Content))
	if result.Truncated {
		nextOffset := result.NextOffset
		if nextOffset <= offset {
			nextOffset = offset + max(tr.OutputLines, 1)
		}
		tr.Content += fmt.Sprintf("\n[Use offset=%d to continue reading]", nextOffset)
	}
	return tr.Content, nil
}

func readAllForEdit(ctx context.Context, host Host, path string) ([]byte, error) {
	if reader, ok := host.(LineOrientedReaderHost); ok {
		return reader.ReadAllFile(ctx, path)
	}
	result, err := host.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return nil, err
	}
	return result.Content, nil
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

func toolIntArg(args map[string]any, key string, defaultVal int) int {
	v, ok := args[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int32:
		return int(n)
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}
