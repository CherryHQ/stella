package boxshclient

import (
	"fmt"
	"strings"
	"time"

	"github.com/vaayne/anna/pkg/tools"
)

// NormalizeResult contains normalized output for Anna tools.
type NormalizeResult struct {
	// Content is the main text output for the tool.
	Content string

	// IsError indicates if this is an error result.
	IsError bool

	// Metadata contains additional information (exit codes, timing, etc.).
	Metadata map[string]any
}

// Normalizer converts boxsh RPC responses into Anna-compatible tool outputs.
type Normalizer struct {
	// MaxOutputLen limits the length of normalized output before truncation.
	// 0 means use default (50KB as per tools.TruncateTail).
	MaxOutputLen int

	// IncludeTimestamps determines whether to include timing info in output.
	IncludeTimestamps bool
}

// NewNormalizer creates a default normalizer with sensible defaults.
func NewNormalizer() *Normalizer {
	return &Normalizer{
		MaxOutputLen:      0, // use default
		IncludeTimestamps: true,
	}
}

// NormalizeExec converts an ExecResult to Anna-compatible output.
func (n *Normalizer) NormalizeExec(result *ExecResult, elapsed time.Duration) *NormalizeResult {
	// Combine stdout and stderr.
	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += result.Stderr
	}

	// Apply truncation.
	var truncated string
	var outputLines int
	if n.MaxOutputLen > 0 {
		// Custom truncation.
		if len(output) > n.MaxOutputLen {
			truncated = output[:n.MaxOutputLen] + "\n[...truncated]"
			outputLines = strings.Count(output[:n.MaxOutputLen], "\n")
		} else {
			truncated = output
			outputLines = strings.Count(output, "\n")
		}
	} else {
		// Use standard tool truncation.
		tr := tools.TruncateTail(output)
		truncated = tr.Content
		outputLines = tr.OutputLines
	}

	// Add metadata footer.
	if n.IncludeTimestamps {
		truncated += formatMetadataFooter(result.ExitCode, elapsed)
	}

	metadata := map[string]any{
		"exit_code":    result.ExitCode,
		"elapsed_ms":   elapsed.Milliseconds(),
		"output_lines": outputLines,
		"stdout_bytes": len(result.Stdout),
		"stderr_bytes": len(result.Stderr),
	}

	return &NormalizeResult{
		Content:  truncated,
		IsError:  result.ExitCode != 0,
		Metadata: metadata,
	}
}

// NormalizeRead converts a ReadResult to Anna-compatible output.
func (n *Normalizer) NormalizeRead(result *ReadResult, filePath string, requestedOffset int) *NormalizeResult {
	content := result.Content

	// Apply head truncation for large content.
	var truncated string
	var outputLines int
	tr := tools.TruncateHead(content)
	truncated = tr.Content
	outputLines = tr.OutputLines

	// Add pagination hint if there are more lines.
	// Ensure pagination advances by at least 1 line to avoid infinite loops
	// (e.g., when a single line exceeds the byte limit and OutputLines == 0).
	linesConsumed := max(outputLines, 1)
	lastLineShown := requestedOffset + linesConsumed - 1
	if lastLineShown < result.TotalLines {
		hint := fmt.Sprintf("\n[Use offset=%d to continue reading]", lastLineShown+1)
		truncated += hint
	}

	// Add truncation indicator if content was truncated.
	if result.Truncated && !strings.HasSuffix(truncated, "...]") {
		truncated += "\n[Content truncated - use offset/limit to paginate]"
	}

	metadata := map[string]any{
		"file_path":   filePath,
		"total_lines": result.TotalLines,
		"shown_lines": outputLines,
		"truncated":   result.Truncated,
	}

	return &NormalizeResult{
		Content:  truncated,
		IsError:  false,
		Metadata: metadata,
	}
}

// NormalizeWrite converts a WriteResult to Anna-compatible output.
func (n *Normalizer) NormalizeWrite(result *WriteResult) *NormalizeResult {
	content := fmt.Sprintf("Wrote %s (%d bytes)", result.Path, result.BytesWritten)

	metadata := map[string]any{
		"path":          result.Path,
		"bytes_written": result.BytesWritten,
	}

	return &NormalizeResult{
		Content:  content,
		IsError:  false,
		Metadata: metadata,
	}
}

// NormalizeEdit converts an EditResult to Anna-compatible output.
func (n *Normalizer) NormalizeEdit(result *EditResult) *NormalizeResult {
	var content string
	if result.Replacements == 1 {
		content = fmt.Sprintf("Edited %s", result.Path)
	} else {
		content = fmt.Sprintf("Edited %s (%d replacements)", result.Path, result.Replacements)
	}

	metadata := map[string]any{
		"path":         result.Path,
		"replacements": result.Replacements,
	}

	return &NormalizeResult{
		Content:  content,
		IsError:  false,
		Metadata: metadata,
	}
}

// NormalizeError converts an error into Anna-compatible output.
func (n *Normalizer) NormalizeError(err error, toolName string) *NormalizeResult {
	content := fmt.Sprintf("%s: %v", toolName, err)

	return &NormalizeResult{
		Content: content,
		IsError: true,
		Metadata: map[string]any{
			"error_type": "execution_error",
			"tool":       toolName,
		},
	}
}

// formatMetadataFooter formats execution metadata as a footer line.
func formatMetadataFooter(exitCode int, elapsed time.Duration) string {
	return fmt.Sprintf("\n[exit:%d | %s]", exitCode, formatDuration(elapsed))
}

// formatDuration formats a duration for human readability.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return fmt.Sprintf("%dµs", d.Microseconds())
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
}
