package rtk

import (
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/vaayne/anna/pkg/hooks"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
)

func init() {
	pluginhooks.Register("rtk", func() hooks.HookPlugin { return &Hook{} })
}

// Hook rewrites bash commands via the rtk binary.
type Hook struct{}

func (h *Hook) Name() string  { return "rtk" }
func (h *Hook) Priority() int { return 100 }

func (h *Hook) OnPreToolCall(_ context.Context, hctx *hooks.PreToolCallContext) (hooks.PreToolCallResult, error) {
	if hctx.ToolName != "bash" {
		return hooks.PreToolCallResult{}, nil
	}
	command, ok := hctx.Arguments["command"].(string)
	if !ok || command == "" {
		return hooks.PreToolCallResult{}, nil
	}
	rewritten := wrapWithRTK(command)
	if rewritten == command {
		return hooks.PreToolCallResult{}, nil
	}
	args := maps.Clone(hctx.Arguments)
	args["command"] = rewritten
	return hooks.PreToolCallResult{Arguments: args}, nil
}

// toolsBinDir is set by SetToolsBinDir before the first hook invocation.
// The runner calls this after extracting embedded tools.
var toolsBinDir string

// SetToolsBinDir configures the directory where embedded tool binaries live.
// Must be called before the first hook invocation (typically during runner setup).
func SetToolsBinDir(dir string) { toolsBinDir = dir }

// rtkPath caches the resolved rtk binary path (empty if not found).
var rtkPath = sync.OnceValue(func() string {
	if toolsBinDir != "" {
		p := filepath.Join(toolsBinDir, "rtk")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("rtk"); err == nil {
		return p
	}
	return ""
})

// wrapWithRTK uses "rtk rewrite" to determine how to wrap the command.
func wrapWithRTK(command string) string {
	rtk := rtkPath()
	if rtk == "" {
		return command
	}
	out, err := exec.Command(rtk, "rewrite", command).Output()
	if err != nil {
		return command
	}
	if rewritten := strings.TrimSpace(string(out)); rewritten != "" {
		return rewritten
	}
	return command
}
