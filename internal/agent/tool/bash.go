package tool

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/internal/toolspec"
)

// BashTool executes bash commands.
type BashTool struct {
	workDir string
}

func (t *BashTool) Definition() toolspec.Definition {
	return toolspec.Definition{
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

	env := envWithToolsBin()
	command = wrapWithRTK(command, env)

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
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		tr := TruncateTail(result)
		tr.Content += formatMetadataFooter(exitCode, elapsed)
		return tr.Content, fmt.Errorf("bash: exit code %d", exitCode)
	}
	tr := TruncateTail(result)
	tr.Content += formatMetadataFooter(0, elapsed)
	return tr.Content, nil
}

func formatMetadataFooter(exitCode int, elapsed time.Duration) string {
	return fmt.Sprintf("\n[exit:%d | %s]", exitCode, formatDuration(elapsed))
}

// rtkPath caches the resolved rtk binary path (empty if not found).
var rtkPath = sync.OnceValue(func() string {
	// Check ANNA_HOME/bin first, then system PATH.
	if p := embedded.ToolPath(config.AnnaHome(), "rtk"); p != "" {
		return p
	}
	if p, err := exec.LookPath("rtk"); err == nil {
		return p
	}
	return ""
})

// wrapWithRTK prefixes the command with "rtk" if rtk is available.
func wrapWithRTK(command string, _ []string) string {
	if rtkPath() == "" {
		return command
	}
	return rtkPath() + " " + command
}

// envWithToolsBin returns the current environment with ANNA_HOME/bin prepended to PATH.
func envWithToolsBin() []string {
	binDir := embedded.BinDir(config.AnnaHome())
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
