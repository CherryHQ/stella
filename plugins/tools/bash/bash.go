package bash

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/tools"
)

func init() {
	pkgplugins.Register("tool/bash", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:          "tool/bash",
			Kind:        "tool",
			Name:        "bash",
			DisplayName: "Bash",
			Description: "Execute shell commands in the current workspace.",
			Capabilities: []string{
				pkgplugins.CapabilityTool,
			},
		})
		host.AddTool(pkgplugins.ToolSpec{
			PluginID:    "tool/bash",
			Name:        "bash",
			Description: "Execute bash commands.",
			Required:    true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return NewBashTool(ctx.Paths.ProjectRoot, ctx.Paths.ToolsBinDir), nil
			},
		})
	}))
}

// BashTool executes bash commands.
type BashTool struct {
	projectRoot string
	binDir      string
}

// NewBashTool creates a BashTool with the given project root and
// optional tools bin directory (prepended to PATH).
// If projectRoot is empty, commands run with no explicit working directory.
func NewBashTool(projectRoot, binDir string) *BashTool {
	return &BashTool{projectRoot: projectRoot, binDir: binDir}
}

func (t *BashTool) Definition() tools.Definition {
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
				"timeout": map[string]any{
					"type":        "integer",
					"description": "Timeout in seconds (optional, no default timeout).",
				},
			},
			"required": []string{"command"},
		},
	}
}

func (t *BashTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", fmt.Errorf("bash: command is required")
	}

	timeout := bashIntArg(args, "timeout", 0)

	env := envWithToolsBin(t.binDir)

	execCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(execCtx, "bash", "-c", command)
	if t.projectRoot != "" {
		cmd.Dir = t.projectRoot
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)
	if cancel != nil && execCtx.Err() == nil {
		cancel()
	}

	result := stdout.String()
	if stderr.Len() > 0 {
		if result != "" {
			result += "\n"
		}
		result += stderr.String()
	}

	if err != nil {
		tr := tools.TruncateTail(result)

		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			tr.Content += formatMetadataFooter(124, elapsed)
			tr.Content += fmt.Sprintf("\n[Command timed out after %ds]", timeout)
			return tr.Content, fmt.Errorf("bash: command timed out after %d seconds", timeout)
		}
		if errors.Is(execCtx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			tr.Content += formatMetadataFooter(130, elapsed)
			tr.Content += "\n[Command aborted]"
			return tr.Content, fmt.Errorf("bash: command aborted")
		}

		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		tr.Content += formatMetadataFooter(exitCode, elapsed)
		return tr.Content, fmt.Errorf("bash: exit code %d", exitCode)
	}
	tr := tools.TruncateTail(result)
	tr.Content += formatMetadataFooter(0, elapsed)
	return tr.Content, nil
}

func formatMetadataFooter(exitCode int, elapsed time.Duration) string {
	return fmt.Sprintf("\n[exit:%d | %s]", exitCode, formatDuration(elapsed))
}

// envWithToolsBin returns the current environment with binDir prepended to PATH.
// If binDir is empty the environment is returned unchanged.
func envWithToolsBin(binDir string) []string {
	if binDir == "" {
		return os.Environ()
	}
	env := os.Environ()
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			env[i] = "PATH=" + binDir + string(os.PathListSeparator) + e[5:]
			return env
		}
	}
	return append(env, "PATH="+binDir)
}

func bashIntArg(args map[string]any, key string, defaultVal int) int {
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
