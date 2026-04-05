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
	pluginhooks.Register("rtk", pluginhooks.Registration{
		Factory: func(bc pluginhooks.BuildContext) (hooks.HookPlugin, error) {
			return NewHook(bc.ToolsBinDir), nil
		},
	})
}

// Hook rewrites bash commands via the rtk binary.
type Hook struct {
	toolsBinDir string
}

// NewHook creates a Hook that looks for the rtk binary in the given directory.
func NewHook(toolsBinDir string) *Hook {
	return &Hook{toolsBinDir: toolsBinDir}
}

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
	rewritten := h.wrapWithRTK(command)
	if rewritten == command {
		return hooks.PreToolCallResult{}, nil
	}
	args := maps.Clone(hctx.Arguments)
	args["command"] = rewritten
	return hooks.PreToolCallResult{Arguments: args}, nil
}

var (
	rtkPathOnce sync.Once
	rtkPathVal  string
)

// resolveRTKPath lazily resolves the rtk binary path, checking the given
// bin directory first, then $PATH.
func resolveRTKPath(binDir string) string {
	rtkPathOnce.Do(func() {
		if binDir != "" {
			p := filepath.Join(binDir, "rtk")
			if _, err := os.Stat(p); err == nil {
				rtkPathVal = p
				return
			}
		}
		if p, err := exec.LookPath("rtk"); err == nil {
			rtkPathVal = p
		}
	})
	return rtkPathVal
}

// wrapWithRTK uses "rtk rewrite" to determine how to wrap the command.
func (h *Hook) wrapWithRTK(command string) string {
	rtk := resolveRTKPath(h.toolsBinDir)
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
