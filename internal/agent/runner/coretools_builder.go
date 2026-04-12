package runner

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/vaayne/anna/internal/sandbox"
	"github.com/vaayne/anna/internal/sandbox/boxshclient"
	"github.com/vaayne/anna/pkg/tools"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

// CoreToolsBuilderWithSandbox creates a CoreToolsBuilder that uses host-backed
// tools when a sandbox session is available, falling back to the provided
// delegate otherwise.
func CoreToolsBuilderWithSandbox(delegate CoreToolsBuilder, session *runnerSession) CoreToolsBuilder {
	return func(bc plugintools.BuildContext) []tools.Tool {
		if session == nil || session.Session() == nil || session.Session().Host() == nil {
			if delegate != nil {
				return delegate(bc)
			}
			return nil
		}
		if session.Policy().Backend != "boxsh" {
			if delegate != nil {
				return delegate(bc)
			}
			return nil
		}
		return buildSandboxCoreTools(session, bc)
	}
}

// buildSandboxCoreTools creates core tools using the active sandbox host.
func buildSandboxCoreTools(session *runnerSession, bc plugintools.BuildContext) []tools.Tool {
	host := session.Session().Host()
	if host == nil {
		return nil
	}

	if session.Policy().Backend != "boxsh" {
		return nil
	}

	return []tools.Tool{
		&hostBashTool{host: host, normalizer: boxshclient.NewNormalizer(), toolsBinDir: bc.ToolsBinDir},
		&hostReadTool{host: host},
		&hostWriteTool{host: host},
		&hostEditTool{host: host},
	}
}

type hostBashTool struct {
	host        sandbox.Host
	normalizer  *boxshclient.Normalizer
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
	result, err := t.host.Exec(ctx, command, sandbox.ExecOptions{Timeout: time.Duration(intArg(args, "timeout", 0)) * time.Second, Env: env})
	if err != nil {
		norm := t.normalizer.NormalizeError(err, "bash")
		return norm.Content, fmt.Errorf("bash: %w", err)
	}

	norm := t.normalizer.NormalizeExec(&boxshclient.ExecResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	}, time.Since(start))
	if norm.IsError {
		return norm.Content, fmt.Errorf("bash: exit code %d", result.ExitCode)
	}
	return norm.Content, nil
}

type hostReadTool struct{ host sandbox.Host }

func (t *hostReadTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "read",
		Description: "Read the contents of a file. Output is truncated to 2000 lines or 50KB. Use offset and limit to paginate through large files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string", "description": "Absolute or relative path to the file to read."},
				"offset":    map[string]any{"type": "integer", "description": "Line number to start reading from (1-based). Defaults to 1."},
				"limit":     map[string]any{"type": "integer", "description": "Maximum number of lines to read. Defaults to all lines."},
			},
			"required": []string{"file_path"},
		},
	}
}

func (t *hostReadTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, ok := args["file_path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("read: file_path is required")
	}

	offset := max(intArg(args, "offset", 1), 1)
	limit := intArg(args, "limit", 0)

	result, err := t.host.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	lines := strings.SplitAfter(string(result.Content), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || offset > len(lines) {
		return "", nil
	}

	selected := lines[offset-1:]
	if limit > 0 && len(selected) > limit {
		selected = selected[:limit]
	}

	content := strings.Join(selected, "")
	tr := tools.TruncateHead(content)
	content = tr.Content
	lastLineShown := offset + max(tr.OutputLines, 1) - 1
	if limit > 0 && offset-1+limit < len(lines) {
		lastLineShown = offset + len(selected) - 1
	}
	if lastLineShown < len(lines) {
		content += fmt.Sprintf("\n[Use offset=%d to continue reading]", lastLineShown+1)
	}
	return content, nil
}

type hostWriteTool struct{ host sandbox.Host }

func (t *hostWriteTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{"type": "string", "description": "Path to the file to create or overwrite."},
				"content":   map[string]any{"type": "string", "description": "The full content to write to the file."},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

func (t *hostWriteTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["file_path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return "", fmt.Errorf("write: file_path is required")
	}

	result, err := t.host.WriteFile(ctx, path, []byte(content))
	if err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", path, result.BytesWritten), nil
}

type hostEditTool struct{ host sandbox.Host }

func (t *hostEditTool) Definition() tools.Definition {
	return tools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path":  map[string]any{"type": "string", "description": "Path to the file to edit."},
				"old_string": map[string]any{"type": "string", "description": "The exact text to find and replace. Must match the file content exactly."},
				"new_string": map[string]any{"type": "string", "description": "The replacement text."},
			},
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}
}

func (t *hostEditTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["file_path"].(string)
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	if path == "" {
		return "", fmt.Errorf("edit: file_path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}

	readResult, err := t.host.ReadFile(ctx, path, 0, 0)
	if err != nil {
		return "", fmt.Errorf("edit: read %s: %w", path, err)
	}
	count := strings.Count(string(readResult.Content), oldStr)
	if count == 0 {
		return "", fmt.Errorf("edit: old_string not found in %s", path)
	}
	if count > 1 {
		return "", fmt.Errorf("edit: old_string matches %d times in %s (must be unique)", count, path)
	}

	result, err := t.host.EditFile(ctx, path, []sandbox.Edit{{OldText: oldStr, NewText: newStr}})
	if err != nil {
		return "", fmt.Errorf("edit %s: %w", path, err)
	}
	if result.AppliedEdits == 1 {
		return fmt.Sprintf("Edited %s", path), nil
	}
	return fmt.Sprintf("Edited %s (%d replacements)", path, result.AppliedEdits), nil
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
	case int32:
		return int(n)
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}
