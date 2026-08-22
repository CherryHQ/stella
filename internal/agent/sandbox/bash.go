package sandbox

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

func bashDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "bash",
		Description: "Execute a bash command — git, package managers, system tools, and fd/rg searches. Do not use it to read or write file contents; use the read/write/edit tools for that. Prefer fd/rg over find/grep. Never print secret values.",
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

func newBashTool(host pkgsandbox.Session, projectRoot string, sessionSecretValues *SessionSecretValues) pkgtools.Tool {
	return &hostBashTool{host: host, normalizer: newToolNormalizer(), projectRoot: projectRoot, sessionSecretValues: sessionSecretValues}
}

type hostBashTool struct {
	host                pkgsandbox.Session
	normalizer          *toolNormalizer
	projectRoot         string
	sessionSecretValues *SessionSecretValues
}

func (t *hostBashTool) Definition() pkgtools.Definition { return bashDefinition() }

func (t *hostBashTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok || command == "" {
		return "", fmt.Errorf("bash: command is required")
	}
	start := time.Now()
	timeoutSeconds := toolIntArg(args, "timeout", 0)
	execOpts := pkgsandbox.ExecOptions{Timeout: time.Duration(timeoutSeconds) * time.Second}
	if t.projectRoot != "" {
		execOpts.Cwd = t.projectRoot
	}
	result, err := t.host.Exec(ctx, command, execOpts)
	secretValues := t.sessionSecretValueList()
	if err != nil {
		norm := t.normalizer.NormalizeError(err, "bash")
		return redactSecretValues(norm.Content, secretValues), fmt.Errorf("bash: %w", err)
	}
	if timeoutSeconds > 0 && result.ExitCode == -1 {
		content := fmt.Sprintf("bash: command timed out after %d seconds\n[exit:124 | %s]", timeoutSeconds, formatToolDuration(time.Since(start)))
		return redactSecretValues(content, secretValues), fmt.Errorf("bash: command timed out after %d seconds", timeoutSeconds)
	}

	norm := t.normalizer.NormalizeExec(result, time.Since(start))
	if norm.IsError {
		// A negative code is the kill sentinel, not a status the command chose:
		// a signal, or a sandbox-policy deadline the caller never asked for and
		// so never reaches the explicit-timeout branch above. Only a status the
		// command actually returned is a command exit.
		if result.ExitCode < 0 {
			return redactSecretValues(norm.Content, secretValues), fmt.Errorf("bash: command was killed before it exited (code %d)", result.ExitCode)
		}
		// Typed, not formatted: the command ran and answered. Consumers that
		// must tell that apart from a tool failure read the type, never the text.
		return redactSecretValues(norm.Content, secretValues), &ai.CommandExitError{Tool: "bash", ExitCode: result.ExitCode}
	}
	return redactSecretValues(norm.Content, secretValues), nil
}

func (t *hostBashTool) sessionSecretValueList() []string {
	return t.sessionSecretValues.Values()
}

func redactSecretValues(content string, values []string) string {
	values = append([]string(nil), values...)
	values = slices.DeleteFunc(values, func(value string) bool { return value == "" })
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		content = strings.ReplaceAll(content, value, "[REDACTED_SECRET]")
	}
	return content
}
