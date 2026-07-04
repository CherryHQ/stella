package tools

import (
	"context"
	"fmt"
	"maps"
	"os"
	"sort"
	"strings"
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
				"secrets": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional vault secret names to inject into this command only."},
			},
			"required": []string{"command"},
		},
	}
}

type ExecSecretResolver interface {
	ResolveExecSecrets(ctx context.Context, names []string, command string) (map[string]string, []string, error)
	DeclarableSecretNames(ctx context.Context) ([]string, error)
}

func newBashTool(host sandbox.Host, toolsBinDir, projectRoot string, secretResolver ExecSecretResolver) pkgtools.Tool {
	return &hostBashTool{host: host, normalizer: newToolNormalizer(), toolsBinDir: toolsBinDir, projectRoot: projectRoot, secretResolver: secretResolver}
}

type hostBashTool struct {
	host           sandbox.Host
	normalizer     *toolNormalizer
	toolsBinDir    string
	projectRoot    string
	secretResolver ExecSecretResolver
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
	secretNames, err := toolStringSliceArg(args, "secrets")
	if err != nil {
		return "", err
	}
	secretEnv := map[string]string{}
	var valid []string
	if len(secretNames) > 0 {
		if t.secretResolver == nil {
			return "", fmt.Errorf("bash: secrets are not available in this session")
		}
		secretEnv, valid, err = t.secretResolver.ResolveExecSecrets(ctx, secretNames, command)
		if err != nil {
			return "", fmt.Errorf("bash: %w (valid declarable secrets: %s)", err, strings.Join(valid, ", "))
		}
		maps.Copy(env, secretEnv)
	}

	// Opt the CLI into emitting renderable-reference sentinels: when the agent
	// runs `stella task/goal/recally create`, the command announces the new
	// entity on stderr so the chat can render a rich card instead of a UUID.
	env["STELLA_RENDERABLE_REFS"] = "1"
	execOpts := sandbox.ExecOptions{Timeout: time.Duration(timeoutSeconds) * time.Second, Env: env}
	if t.projectRoot != "" {
		execOpts.Cwd = t.projectRoot
	}
	result, err := t.host.Exec(ctx, command, execOpts)
	if err != nil {
		norm := t.normalizer.NormalizeError(err, "bash")
		return redactSecretValues(norm.Content, secretEnv), fmt.Errorf("bash: %w", err)
	}
	if timeoutSeconds > 0 && result.ExitCode == -1 {
		content := fmt.Sprintf("bash: command timed out after %d seconds\n[exit:124 | %s]", timeoutSeconds, formatToolDuration(time.Since(start)))
		return redactSecretValues(content, secretEnv), fmt.Errorf("bash: command timed out after %d seconds", timeoutSeconds)
	}

	norm := t.normalizer.NormalizeExec(result, time.Since(start))
	if norm.IsError {
		content := norm.Content + t.undeclaredSecretHint(ctx, command, secretNames)
		return redactSecretValues(content, secretEnv), fmt.Errorf("bash: exit code %d", result.ExitCode)
	}
	return redactSecretValues(norm.Content, secretEnv), nil
}

func (t *hostBashTool) undeclaredSecretHint(ctx context.Context, command string, declared []string) string {
	if t.secretResolver == nil {
		return ""
	}
	names, err := t.secretResolver.DeclarableSecretNames(ctx)
	if err != nil {
		return ""
	}
	declaredSet := make(map[string]bool, len(declared))
	for _, name := range declared {
		declaredSet[name] = true
	}
	var referenced []string
	for _, name := range names {
		if declaredSet[name] || !commandReferencesSecret(command, name) {
			continue
		}
		referenced = append(referenced, name)
	}
	if len(referenced) == 0 {
		return ""
	}
	sort.Strings(referenced)
	return fmt.Sprintf("\nhint: the command references undeclared vault secret(s): %s. Retry with the bash \"secrets\" parameter, e.g. secrets: [%s].", strings.Join(referenced, ", "), quoteSecretNames(referenced))
}

func quoteSecretNames(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("\"%s\"", name))
	}
	return strings.Join(quoted, ", ")
}

func commandReferencesSecret(command, name string) bool {
	if name == "" {
		return false
	}
	if strings.Contains(command, "${"+name+"}") {
		return true
	}
	needle := "$" + name
	for i := 0; i < len(command); {
		idx := strings.Index(command[i:], needle)
		if idx == -1 {
			return false
		}
		end := i + idx + len(needle)
		if end == len(command) || !isEnvNameChar(command[end]) {
			return true
		}
		i = end
	}
	return false
}

func isEnvNameChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func redactSecretValues(content string, env map[string]string) string {
	for _, value := range env {
		if value == "" {
			continue
		}
		content = strings.ReplaceAll(content, value, "[REDACTED_SECRET]")
	}
	return content
}
