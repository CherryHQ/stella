package rtk

import (
	"context"
	"maps"
	"os/exec"
	"strings"

	"github.com/vaayne/anna/pkg/hooks"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func init() {
	pkgplugins.Register("hook/rtk", pkgplugins.PluginFunc(func(host pkgplugins.Host) {
		host.SetInfo(pkgplugins.PluginInfo{
			ID:           "hook/rtk",
			Kind:         "hook",
			Name:         "rtk",
			DisplayName:  "RTK",
			Description:  "Rewrite bash commands through the rtk binary.",
			AdminVisible: true,
			Capabilities: []string{
				pkgplugins.CapabilityHook,
			},
		})
		host.AddHook(pkgplugins.HookSpec{
			PluginID: "hook/rtk",
			Name:     "rtk",
			Build: func(ctx pkgplugins.HookContext) (hooks.HookPlugin, error) {
				return NewHook(ctx.ToolsBinDir), nil
			},
		})
	}))
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

// wrapWithRTK uses "rtk rewrite" to determine how to wrap the command.
func (h *Hook) wrapWithRTK(command string) string {
	rtk := pkgplugins.ResolveBinary(h.toolsBinDir, "rtk")
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
