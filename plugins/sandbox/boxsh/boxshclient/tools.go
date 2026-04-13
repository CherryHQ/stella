package boxshclient

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// ExecParams are parameters for the Exec method.
type ExecParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// ExecResult is the result from the Exec method.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// ReadParams are parameters for the Read method.
type ReadParams struct {
	FilePath string `json:"path"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

// ReadResult is the result from the Read method.
type ReadResult struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

// WriteParams are parameters for the Write method.
type WriteParams struct {
	FilePath string `json:"path"`
	Content  string `json:"content"`
}

// WriteResult is the result from the Write method.
type WriteResult struct {
	BytesWritten int    `json:"bytes_written"`
	Path         string `json:"path"`
}

// EditParams are parameters for the Edit method.
type EditParams struct {
	FilePath string     `json:"path"`
	Edits    []EditSpec `json:"edits"`
}

// EditSpec is one edit operation accepted by boxsh.
type EditSpec struct {
	OldText string `json:"oldText"`
	NewText string `json:"newText"`
}

// EditResult is the result from the Edit method.
type EditResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Diff         string `json:"diff,omitempty"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolCallResult struct {
	Content           []mcpTextContent `json:"content"`
	StructuredContent json.RawMessage  `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

type mcpExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

type mcpReadResult struct {
	Truncation struct {
		LineCount int  `json:"line_count"`
		Truncated bool `json:"truncated"`
	} `json:"truncation"`
}

type mcpEditResult struct {
	Diff             string `json:"diff"`
	FirstChangedLine int    `json:"firstChangedLine"`
}

func (c *Client) toolCall(ctx context.Context, name string, arguments any) (*mcpToolCallResult, error) {
	var result mcpToolCallResult
	if err := c.call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": arguments,
	}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func joinToolText(content []mcpTextContent) string {
	parts := make([]string, 0, len(content))
	for _, block := range content {
		if block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

func toolCallError(name string, result *mcpToolCallResult) error {
	text := strings.TrimSpace(joinToolText(result.Content))
	if text == "" {
		text = name + " failed"
	}
	return fmt.Errorf("boxshclient %s: %s", name, text)
}

// Exec runs a bash command in the sandbox.
func (c *Client) Exec(ctx context.Context, params ExecParams) (*ExecResult, error) {
	result, err := c.toolCall(ctx, "bash", params)
	if err != nil {
		return nil, fmt.Errorf("boxshclient exec: %w", err)
	}

	var structured mcpExecResult
	if len(result.StructuredContent) > 0 {
		if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
			return nil, fmt.Errorf("boxshclient exec: decode structured content: %w", err)
		}
		return &ExecResult{Stdout: structured.Stdout, Stderr: structured.Stderr, ExitCode: structured.ExitCode}, nil
	}
	if result.IsError {
		return nil, toolCallError("exec", result)
	}
	return &ExecResult{Stdout: joinToolText(result.Content), ExitCode: 0}, nil
}

// Read reads a file from the sandbox.
func (c *Client) Read(ctx context.Context, params ReadParams) (*ReadResult, error) {
	result, err := c.toolCall(ctx, "read", params)
	if err != nil {
		return nil, fmt.Errorf("boxshclient read: %w", err)
	}
	if result.IsError {
		return nil, toolCallError("read", result)
	}

	content := joinToolText(result.Content)
	readResult := &ReadResult{Content: content}
	if len(result.StructuredContent) > 0 {
		var structured mcpReadResult
		if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
			return nil, fmt.Errorf("boxshclient read: decode structured content: %w", err)
		}
		readResult.TotalLines = structured.Truncation.LineCount
		readResult.Truncated = structured.Truncation.Truncated
	} else {
		readResult.TotalLines = strings.Count(content, "\n")
		if content != "" && !strings.HasSuffix(content, "\n") {
			readResult.TotalLines++
		}
	}
	return readResult, nil
}

var writtenBytesRE = regexp.MustCompile(`written\s+(\d+)\s+bytes`)

// Write writes content to a file in the sandbox.
func (c *Client) Write(ctx context.Context, params WriteParams) (*WriteResult, error) {
	result, err := c.toolCall(ctx, "write", params)
	if err != nil {
		return nil, fmt.Errorf("boxshclient write: %w", err)
	}
	if result.IsError {
		return nil, toolCallError("write", result)
	}

	bytesWritten := len(params.Content)
	if match := writtenBytesRE.FindStringSubmatch(joinToolText(result.Content)); len(match) == 2 {
		var parsed int
		_, _ = fmt.Sscanf(match[1], "%d", &parsed)
		if parsed >= 0 {
			bytesWritten = parsed
		}
	}
	return &WriteResult{BytesWritten: bytesWritten, Path: params.FilePath}, nil
}

// Edit applies string replacements to a file in the sandbox.
func (c *Client) Edit(ctx context.Context, params EditParams) (*EditResult, error) {
	result, err := c.toolCall(ctx, "edit", params)
	if err != nil {
		return nil, fmt.Errorf("boxshclient edit: %w", err)
	}
	if result.IsError {
		return nil, toolCallError("edit", result)
	}

	editResult := &EditResult{Path: params.FilePath, Replacements: len(params.Edits)}
	if len(result.StructuredContent) > 0 {
		var structured mcpEditResult
		if err := json.Unmarshal(result.StructuredContent, &structured); err != nil {
			return nil, fmt.Errorf("boxshclient edit: decode structured content: %w", err)
		}
		editResult.Diff = structured.Diff
		if structured.FirstChangedLine == 0 {
			editResult.Replacements = 0
		}
	}
	return editResult, nil
}

// ListParams are parameters for the List method (directory listing).
type ListParams struct {
	DirPath string `json:"dir_path,omitempty"`
}

// ListResult is the result from the List method.
type ListResult struct {
	Entries []DirEntry `json:"entries"`
}

// DirEntry represents a single directory entry.
type DirEntry struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// List is not supported by boxsh 2.0.1.
func (c *Client) List(context.Context, ListParams) (*ListResult, error) {
	return nil, fmt.Errorf("boxshclient list: unsupported by boxsh 2.0.1")
}

// StatParams are parameters for the Stat method.
type StatParams struct {
	Path string `json:"path"`
}

// StatResult is the result from the Stat method.
type StatResult struct {
	Exists  bool   `json:"exists"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time"`
}

// Stat is not supported by boxsh 2.0.1.
func (c *Client) Stat(context.Context, StatParams) (*StatResult, error) {
	return nil, fmt.Errorf("boxshclient stat: unsupported by boxsh 2.0.1")
}
