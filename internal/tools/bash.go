package tools

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func bashDefinition() pkgtools.Definition {
	return pkgtools.Definition{
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

func newBashTool(host sandbox.Host, toolsBinDir, projectRoot string) pkgtools.Tool {
	return &hostBashTool{host: host, normalizer: newToolNormalizer(), toolsBinDir: toolsBinDir, projectRoot: projectRoot}
}

type hostBashTool struct {
	host        sandbox.Host
	normalizer  *toolNormalizer
	toolsBinDir string
	projectRoot string
}

func (t *hostBashTool) Definition() pkgtools.Definition { return bashDefinition() }

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
