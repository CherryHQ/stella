package sandbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

type toolNormalizeResult struct {
	Content string
	IsError bool
}

type toolNormalizer struct {
	MaxOutputLen      int
	IncludeTimestamps bool
}

func newToolNormalizer() *toolNormalizer {
	return &toolNormalizer{
		IncludeTimestamps: true,
	}
}

func (n *toolNormalizer) NormalizeExec(result pkgsandbox.ExecResult, elapsed time.Duration) toolNormalizeResult {
	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += result.Stderr
	}

	var truncated string
	if n.MaxOutputLen > 0 {
		if len(output) > n.MaxOutputLen {
			truncated = output[:n.MaxOutputLen] + "\n[...truncated]"
		} else {
			truncated = output
		}
	} else {
		truncated = pkgtools.TruncateTail(output).Content
	}

	if n.IncludeTimestamps {
		truncated += fmt.Sprintf("\n[exit:%d | %s]", result.ExitCode, formatToolDuration(elapsed))
	}

	return toolNormalizeResult{
		Content: truncated,
		IsError: result.ExitCode != 0,
	}
}

func (n *toolNormalizer) NormalizeError(err error, toolName string) toolNormalizeResult {
	message := fmt.Sprintf("%s: %v", toolName, err)
	if errors.Is(err, context.DeadlineExceeded) {
		message = fmt.Sprintf("%s: command timed out", toolName)
	} else if errors.Is(err, context.Canceled) {
		message = fmt.Sprintf("%s: command aborted", toolName)
	}
	return toolNormalizeResult{
		Content: message,
		IsError: true,
	}
}

func formatToolDuration(d time.Duration) string {
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
