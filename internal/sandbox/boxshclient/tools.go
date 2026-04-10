package boxshclient

import (
	"context"
	"fmt"
)

// ExecParams are parameters for the Exec method.
type ExecParams struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"` // seconds, 0 = default
}

// ExecResult is the result from the Exec method.
type ExecResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

// Exec runs a bash command in the sandbox.
func (c *Client) Exec(ctx context.Context, params ExecParams) (*ExecResult, error) {
	var result ExecResult
	if err := c.call(ctx, "exec", params, &result); err != nil {
		return nil, fmt.Errorf("boxshclient exec: %w", err)
	}
	return &result, nil
}

// ReadParams are parameters for the Read method.
type ReadParams struct {
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset,omitempty"` // 1-based line number
	Limit    int    `json:"limit,omitempty"`  // max lines to read
}

// ReadResult is the result from the Read method.
type ReadResult struct {
	Content    string `json:"content"`
	TotalLines int    `json:"total_lines"`
	Truncated  bool   `json:"truncated"`
}

// Read reads a file from the sandbox.
func (c *Client) Read(ctx context.Context, params ReadParams) (*ReadResult, error) {
	var result ReadResult
	if err := c.call(ctx, "read", params, &result); err != nil {
		return nil, fmt.Errorf("boxshclient read: %w", err)
	}
	return &result, nil
}

// WriteParams are parameters for the Write method.
type WriteParams struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

// WriteResult is the result from the Write method.
type WriteResult struct {
	BytesWritten int    `json:"bytes_written"`
	Path         string `json:"path"`
}

// Write writes content to a file in the sandbox.
func (c *Client) Write(ctx context.Context, params WriteParams) (*WriteResult, error) {
	var result WriteResult
	if err := c.call(ctx, "write", params, &result); err != nil {
		return nil, fmt.Errorf("boxshclient write: %w", err)
	}
	return &result, nil
}

// EditParams are parameters for the Edit method.
type EditParams struct {
	FilePath  string `json:"file_path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// EditResult is the result from the Edit method.
type EditResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
}

// Edit applies a string replacement to a file in the sandbox.
func (c *Client) Edit(ctx context.Context, params EditParams) (*EditResult, error) {
	var result EditResult
	if err := c.call(ctx, "edit", params, &result); err != nil {
		return nil, fmt.Errorf("boxshclient edit: %w", err)
	}
	return &result, nil
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

// List lists directory contents in the sandbox.
func (c *Client) List(ctx context.Context, params ListParams) (*ListResult, error) {
	var result ListResult
	if err := c.call(ctx, "list", params, &result); err != nil {
		return nil, fmt.Errorf("boxshclient list: %w", err)
	}
	return &result, nil
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
	ModTime string `json:"mod_time"` // RFC3339 format
}

// Stat gets file/directory information in the sandbox.
func (c *Client) Stat(ctx context.Context, params StatParams) (*StatResult, error) {
	var result StatResult
	if err := c.call(ctx, "stat", params, &result); err != nil {
		return nil, fmt.Errorf("boxshclient stat: %w", err)
	}
	return &result, nil
}
