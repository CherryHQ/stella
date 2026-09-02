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

// normalizeExec renders one sandbox exec into the model-facing tool result:
// stderr appended to stdout, tail-truncated by pkg/tools, then the exit/duration
// footer the model reads to decide whether the command worked.
func normalizeExec(result pkgsandbox.ExecResult, elapsed time.Duration) toolNormalizeResult {
	output := result.Stdout
	if result.Stderr != "" {
		if output != "" {
			output += "\n"
		}
		output += result.Stderr
	}

	truncated := pkgtools.TruncateTail(output).Content
	truncated += fmt.Sprintf("\n[exit:%d | %s]", result.ExitCode, formatToolDuration(elapsed))

	return toolNormalizeResult{
		Content: truncated,
		IsError: result.ExitCode != 0,
	}
}

func normalizeToolError(err error, toolName string) toolNormalizeResult {
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
