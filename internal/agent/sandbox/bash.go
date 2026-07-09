package sandbox

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
	"time"

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

func newBashTool(host pkgsandbox.Host, toolsBinDir, projectRoot string, sessionSecretValues *SessionSecretValues) pkgtools.Tool {
	return &hostBashTool{host: host, normalizer: newToolNormalizer(), toolsBinDir: toolsBinDir, projectRoot: projectRoot, sessionSecretValues: sessionSecretValues}
}

type hostBashTool struct {
	host                pkgsandbox.Host
	normalizer          *toolNormalizer
	toolsBinDir         string
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
	env := map[string]string{}
	if t.toolsBinDir != "" {
		env["PATH"] = t.toolsBinDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	secretEnv := map[string]string{}

	execOpts := pkgsandbox.ExecOptions{Timeout: time.Duration(timeoutSeconds) * time.Second, Env: env}
	if t.projectRoot != "" {
		execOpts.Cwd = t.projectRoot
	}
	result, err := t.host.Exec(ctx, command, execOpts)
	redactionEnv, redactionErr := t.redactionEnv(ctx, secretEnv)
	if redactionErr != nil {
		return "", redactionErr
	}
	if err != nil {
		norm := t.normalizer.NormalizeError(err, "bash")
		return redactSecretValues(norm.Content, redactionEnv), fmt.Errorf("bash: %w", err)
	}
	if timeoutSeconds > 0 && result.ExitCode == -1 {
		content := fmt.Sprintf("bash: command timed out after %d seconds\n[exit:124 | %s]", timeoutSeconds, formatToolDuration(time.Since(start)))
		return redactSecretValues(content, redactionEnv), fmt.Errorf("bash: command timed out after %d seconds", timeoutSeconds)
	}

	norm := t.normalizer.NormalizeExec(result, time.Since(start))
	if norm.IsError {
		return redactSecretValues(norm.Content, redactionEnv), fmt.Errorf("bash: exit code %d", result.ExitCode)
	}
	return redactSecretValues(norm.Content, redactionEnv), nil
}

func (t *hostBashTool) redactionEnv(ctx context.Context, secretEnv map[string]string) (map[string]string, error) {
	sessionSecretValues, err := t.sessionSecretValueList(ctx)
	if err != nil {
		return nil, err
	}
	return buildRedactionEnv(secretEnv, sessionSecretValues), nil
}

func (t *hostBashTool) sessionSecretValueList(context.Context) ([]string, error) {
	if t.sessionSecretValues == nil {
		return nil, nil
	}
	return t.sessionSecretValues.Values(), nil
}

func buildRedactionEnv(secretEnv map[string]string, sessionSecretValues []string) map[string]string {
	env := make(map[string]string, len(secretEnv)+len(sessionSecretValues))
	maps.Copy(env, secretEnv)
	for i, value := range sessionSecretValues {
		env[fmt.Sprintf("__SESSION_SECRET_%d", i)] = value
	}
	return env
}

func redactSecretValues(content string, env map[string]string) string {
	values := make([]string, 0, len(env))
	for _, value := range env {
		if value == "" {
			continue
		}
		values = append(values, value)
	}
	sort.SliceStable(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, value := range values {
		content = strings.ReplaceAll(content, value, "[REDACTED_SECRET]")
	}
	return content
}
