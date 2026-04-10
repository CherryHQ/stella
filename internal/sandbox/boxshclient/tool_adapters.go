// Package boxshclient provides boxsh-backed tool adapters for core tools.
package boxshclient

import (
	"context"
	"fmt"
	"time"

	"github.com/vaayne/anna/pkg/tools"
)

// BashAdapter executes bash commands through the boxsh sandbox.
type BashAdapter struct {
	backend    *SharedBackend
	normalizer *Normalizer
	binDir     string // optional tools bin directory for PATH
}

// NewBashAdapter creates a new boxsh-backed bash tool.
func NewBashAdapter(backend *SharedBackend, binDir string) *BashAdapter {
	return &BashAdapter{
		backend:    backend,
		normalizer: NewNormalizer(),
		binDir:     binDir,
	}
}

// Definition returns the tool definition for bash.
func (a *BashAdapter) Definition() tools.Definition {
	return tools.Definition{
		Name:        "bash",
		Description: "Execute a bash command. Use for file operations like ls, rg, find, git, and other shell commands.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The bash command to execute.",
				},
			},
			"required": []string{"command"},
		},
	}
}

// Execute runs a bash command in the sandbox.
func (a *BashAdapter) Execute(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	client := a.backend.Client()
	if client == nil {
		return "", fmt.Errorf("bash: boxsh backend not available")
	}

	// Prepend tools bin to PATH if configured.
	if a.binDir != "" {
		command = fmt.Sprintf("export PATH=%q:$PATH; %s", a.binDir, command)
	}

	start := time.Now()
	result, err := client.Exec(ctx, ExecParams{Command: command})
	elapsed := time.Since(start)

	if err != nil {
		norm := a.normalizer.NormalizeError(err, "bash")
		return norm.Content, fmt.Errorf("bash: %w", err)
	}

	norm := a.normalizer.NormalizeExec(result, elapsed)
	if norm.IsError {
		return norm.Content, fmt.Errorf("bash: exit code %d", result.ExitCode)
	}
	return norm.Content, nil
}

// ReadAdapter reads files through the boxsh sandbox.
type ReadAdapter struct {
	backend    *SharedBackend
	normalizer *Normalizer
}

// NewReadAdapter creates a new boxsh-backed read tool.
func NewReadAdapter(backend *SharedBackend) *ReadAdapter {
	return &ReadAdapter{
		backend:    backend,
		normalizer: NewNormalizer(),
	}
}

// Definition returns the tool definition for read.
func (a *ReadAdapter) Definition() tools.Definition {
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

// Execute reads a file from the sandbox.
func (a *ReadAdapter) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, ok := args["file_path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("read: file_path is required")
	}

	client := a.backend.Client()
	if client == nil {
		return "", fmt.Errorf("read: boxsh backend not available")
	}

	offset := intArg(args, "offset", 1)
	limit := intArg(args, "limit", 0)
	if offset < 1 {
		offset = 1
	}

	result, err := client.Read(ctx, ReadParams{
		FilePath: path,
		Offset:   offset,
		Limit:    limit,
	})
	if err != nil {
		norm := a.normalizer.NormalizeError(err, "read")
		return norm.Content, fmt.Errorf("read %s: %w", path, err)
	}

	norm := a.normalizer.NormalizeRead(result, path, offset)
	return norm.Content, nil
}

// WriteAdapter writes files through the boxsh sandbox.
type WriteAdapter struct {
	backend    *SharedBackend
	normalizer *Normalizer
}

// NewWriteAdapter creates a new boxsh-backed write tool.
func NewWriteAdapter(backend *SharedBackend) *WriteAdapter {
	return &WriteAdapter{
		backend:    backend,
		normalizer: NewNormalizer(),
	}
}

// Definition returns the tool definition for write.
func (a *WriteAdapter) Definition() tools.Definition {
	return tools.Definition{
		Name:        "write",
		Description: "Create a new file or completely overwrite an existing file with the provided content.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
					"type":        "string",
					"description": "Path to the file to create or overwrite.",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "The full content to write to the file.",
				},
			},
			"required": []string{"file_path", "content"},
		},
	}
}

// Execute writes content to a file in the sandbox.
func (a *WriteAdapter) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["file_path"].(string)
	content, _ := args["content"].(string)

	if path == "" {
		return "", fmt.Errorf("write: file_path is required")
	}

	client := a.backend.Client()
	if client == nil {
		return "", fmt.Errorf("write: boxsh backend not available")
	}

	result, err := client.Write(ctx, WriteParams{
		FilePath: path,
		Content:  content,
	})
	if err != nil {
		norm := a.normalizer.NormalizeError(err, "write")
		return norm.Content, fmt.Errorf("write %s: %w", path, err)
	}

	norm := a.normalizer.NormalizeWrite(result)
	return norm.Content, nil
}

// EditAdapter edits files through the boxsh sandbox.
type EditAdapter struct {
	backend    *SharedBackend
	normalizer *Normalizer
}

// NewEditAdapter creates a new boxsh-backed edit tool.
func NewEditAdapter(backend *SharedBackend) *EditAdapter {
	return &EditAdapter{
		backend:    backend,
		normalizer: NewNormalizer(),
	}
}

// Definition returns the tool definition for edit.
func (a *EditAdapter) Definition() tools.Definition {
	return tools.Definition{
		Name:        "edit",
		Description: "Make a surgical edit to a file. The old_string must match exactly (including whitespace and indentation). Use this for targeted changes to existing files.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"file_path": map[string]any{
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
			"required": []string{"file_path", "old_string", "new_string"},
		},
	}
}

// Execute applies edits to a file in the sandbox.
func (a *EditAdapter) Execute(ctx context.Context, args map[string]any) (string, error) {
	path, _ := args["file_path"].(string)
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)

	if path == "" {
		return "", fmt.Errorf("edit: file_path is required")
	}
	if oldStr == "" {
		return "", fmt.Errorf("edit: old_string is required")
	}

	client := a.backend.Client()
	if client == nil {
		return "", fmt.Errorf("edit: boxsh backend not available")
	}

	result, err := client.Edit(ctx, EditParams{
		FilePath:  path,
		OldString: oldStr,
		NewString: newStr,
	})
	if err != nil {
		norm := a.normalizer.NormalizeError(err, "edit")
		return norm.Content, fmt.Errorf("edit %s: %w", path, err)
	}

	norm := a.normalizer.NormalizeEdit(result)
	return norm.Content, nil
}

// intArg extracts an integer argument with a default value.
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
