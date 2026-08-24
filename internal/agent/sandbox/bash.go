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

// bashDescription states the constraints of this environment, not a method for
// working in it. Choosing between sed, python and perl is what bash is for, and
// a tool description is the wrong place to make that choice on the model's
// behalf. What it must carry is what the model cannot discover: the output
// budget it is spending, that spending it on noise is not recoverable because
// the result stays in history, the fidelity it is expected to preserve, the
// token cost that measurement showed it repeatedly paying, and a capability
// that may or may not be installed.
//
// It deliberately does not promise the truncation temp file. That path comes
// from the stellad host's os.CreateTemp, and a Docker or bridge session cannot
// reach host coordinates.
const bashDescription = `Execute a bash command: git, package managers, system tools, fd/rg searches, and all file reading, writing, and editing. Prefer fd/rg over find/grep. Never print secret values.

Working with files, pick whatever fits — shell tools, python, perl. Five things you cannot see from here:

- Output is truncated (by default at 2000 lines or 50KB). Reading a large file whole spends that budget and still shows you a fraction of it, so narrow the command rather than paging through a dump.
- Unknown or binary files are not text. Identify one before dumping it: the noise is bounded, but it spends the whole budget and then rides along in the conversation for every later turn.
- Preserve what you are not changing: line endings, trailing newline, encoding. A read-modify-write through text APIs will silently rewrite every CRLF in a file it was asked to change one line of.
- When iterating on a script, keep it in a file and patch it. Re-sending a whole script in a fresh heredoc every turn was the largest avoidable token cost measured on this workload.
- ` + "`xberg extract FILE --log-level error`" + ` reads pdf, docx, xlsx, pptx, epub and image formats, with --ocr true for images and scans; it is not present in every sandbox, so check command -v xberg first and fall back to whatever else is available.`

func bashDefinition() pkgtools.Definition {
	return pkgtools.Definition{
		Name:        "bash",
		Description: bashDescription,
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
