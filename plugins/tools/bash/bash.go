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
			Sandboxed:   true,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				dir := ctx.WorkDir
				if ctx.UserDataDir != "" {
					dir = ctx.UserDataDir
				}
				return NewBashTool(dir, ctx.ToolsBinDir), nil
			},
		})
	}))
}

// BashTool executes bash commands.
type BashTool struct {
	workDir string
	binDir  string
}

// NewBashTool creates a BashTool with the given working directory and
// optional tools bin directory (prepended to PATH).
func NewBashTool(workDir, binDir string) *BashTool {
	return &BashTool{workDir: workDir, binDir: binDir}
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

	env := envWithToolsBin(t.binDir)

	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if t.workDir != "" {
		cmd.Dir = t.workDir
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	err := cmd.Run()
	elapsed := time.Since(start)

	result := stdout.String()
	if stderr.Len() > 0 {
		if result != "" {
			result += "\n"
		}
		result += stderr.String()
	}

	if err != nil {
		exitCode := 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
		tr := tools.TruncateTail(result)
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
