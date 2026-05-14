package coretools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
)

// New returns the four standard sandbox-backed tools (bash, read, write, edit).
func New(host sandbox.Host, toolsBinDir, projectRoot string) []tools.Tool {
	if host == nil {
		return nil
	}
	return []tools.Tool{
		newBashTool(host, toolsBinDir, projectRoot),
		newReadTool(host, projectRoot),
		newWriteTool(host, projectRoot),
		newEditTool(host, projectRoot),
	}
}

func newBashTool(host sandbox.Host, toolsBinDir, projectRoot string) tools.Tool {
	return &hostBashTool{host: host, normalizer: newToolNormalizer(), toolsBinDir: toolsBinDir, projectRoot: projectRoot}
}

func newReadTool(host sandbox.Host, projectRoot string) tools.Tool {
	return &hostReadTool{host: host, projectRoot: projectRoot}
}

func newWriteTool(host sandbox.Host, projectRoot string) tools.Tool {
	return &hostWriteTool{host: host, projectRoot: projectRoot}
}

func newEditTool(host sandbox.Host, projectRoot string) tools.Tool {
	return &hostEditTool{host: host, projectRoot: projectRoot}
}

func resolveToolPath(host sandbox.Host, projectRoot, path string) (string, error) {
	if projectRoot != "" {
		return tools.ResolveProjectPath(projectRoot, path)
	}
	return host.ResolvePath(path)
}

func resolveWritableToolPath(host sandbox.Host, projectRoot, path string) (string, error) {
	if projectRoot != "" {
		resolved, err := tools.ResolveProjectPath(projectRoot, path)
		if err != nil {
			return "", err
		}
		return host.ResolveWritePath(resolved)
	}
	return host.ResolveWritePath(path)
}

type hostBashTool struct {
	host        sandbox.Host
	normalizer  *toolNormalizer
	toolsBinDir string
	projectRoot string
}

func (t *hostBashTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "bash",
		Description: "Execute a bash command. Use for file operations like ls, rg, find, git, and other shell commands.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string", "description": "The bash command to execute."},
				"timeout": map[string]any{"type": "integer", "description": "Timeout in seconds (optional, no default timeout)."},
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
	timeoutSeconds := toolIntArg(args, "timeout", 0)
	env := map[string]string{}
	if t.toolsBinDir != "" {
		env["PATH"] = t.toolsBinDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	execOpts := sandbox.ExecOptions{Timeout: time.Duration(timeoutSeconds) * time.Second, Env: env}
	if t.projectRoot != "" {
		execOpts.Cwd = t.projectRoot
	}
	result, err := t.host.Exec(ctx, command, execOpts)
	if err != nil {
		norm := t.normalizer.NormalizeError(err, "bash")
		return norm.Content, fmt.Errorf("bash: %w", err)
	}
	if timeoutSeconds > 0 && result.ExitCode == -1 {
		content := fmt.Sprintf("bash: command timed out after %d seconds\n[exit:124 | %s]", timeoutSeconds, formatToolDuration(time.Since(start)))
		return content, fmt.Errorf("bash: command timed out after %d seconds", timeoutSeconds)
	}

	norm := t.normalizer.NormalizeExec(result, time.Since(start))
	if norm.IsError {
		return norm.Content, fmt.Errorf("bash: exit code %d", result.ExitCode)
	}
	return norm.Content, nil
}

type hostReadTool struct {
	host        sandbox.Host
	projectRoot string
}

func (t *hostReadTool) Definition() tools.Definition {
	return tools.Definition{
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

func (t *hostReadTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := tools.StringArg(args, "path")
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
		if len(sample) > 0 && tools.IsBinary(string(sample)) {
			return "", fmt.Errorf("read %s: binary file detected — use bash with xxd, file, or other tools to inspect binary content", path)
		}
	}

	paged, totalLines := paginateReadContent(string(content), offset, limit)
	if totalLines == 0 {
		return "", nil
	}

	tr := tools.TruncateHead(paged)
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

type hostWriteTool struct {
	host        sandbox.Host
	projectRoot string
}

func (t *hostWriteTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Path to the file to create or overwrite."},
				"content": map[string]any{"type": "string", "description": "The full content to write to the file."},
			},
			"required": []string{"path", "content"},
		},
	}
}

func (t *hostWriteTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := tools.StringArg(args, "path")
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("write: path is required")
	}

	resolvedPath, err := resolveWritableToolPath(t.host, t.projectRoot, path)
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return "", fmt.Errorf("write: mkdir %s: %w", filepath.Dir(resolvedPath), err)
	}

	if err := os.WriteFile(resolvedPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", path, len(content)), nil
}

type hostEditTool struct {
	host        sandbox.Host
	projectRoot string
}

func (t *hostEditTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":       map[string]any{"type": "string", "description": "Path to the file to edit."},
				"old_string": map[string]any{"type": "string", "description": "The exact text to find and replace. Must match the file content exactly."},
				"new_string": map[string]any{"type": "string", "description": "The replacement text."},
			},
			"required": []string{"path", "old_string", "new_string"},
		},
	}
}

func (t *hostEditTool) Execute(_ context.Context, args map[string]any) (string, error) {
	path := tools.StringArg(args, "path")
	oldStr := tools.StringArg(args, "old_string")
	newStr := tools.StringArg(args, "new_string")
	if path == "" {
		return "", fmt.Errorf("edit: path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}

	resolvedPath, err := resolveWritableToolPath(t.host, t.projectRoot, path)
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}

	raw, err := os.ReadFile(resolvedPath)
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
	if err := os.WriteFile(resolvedPath, []byte(updated), 0o644); err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	return fmt.Sprintf("Edited %s", path), nil
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
