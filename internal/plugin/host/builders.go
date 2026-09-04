package host

import (
	"context"
	"sort"

	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/tools"
)

func (h *Host) BuildEnabledTools(ctx context.Context, bc pkgplugins.ToolBuildContext) []tools.Tool {
	h.mu.RLock()
	regs := make([]pkgplugins.ToolSpec, 0, len(h.toolRegs))
	for _, reg := range h.toolRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })
	var out []tools.Tool
	for _, reg := range regs {
		if reg.Build == nil {
			continue
		}
		if !reg.Required {
			state, err := h.DesiredState(ctx, reg.PluginID)
			if err != nil || !state.Enabled {
				continue
			}
		}
		t, err := reg.Build(pkgplugins.ToolContext{
			Platform: h.platform(reg.PluginID),
			Runtime:  bc.Runtime,
		})
		if err == nil && t != nil {
			out = append(out, t)
		}
	}
	return out
}

func (h *Host) BuildEnabledHooks(ctx context.Context, toolsBinDir string) []hooks.HookPlugin {
	h.mu.RLock()
	regs := make([]pkgplugins.HookSpec, 0, len(h.hookRegs))
	for _, reg := range h.hookRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })
	var out []hooks.HookPlugin
	for _, reg := range regs {
		state, err := h.DesiredState(ctx, reg.PluginID)
		if err != nil || !state.Enabled || reg.Build == nil {
			continue
		}
		item, err := reg.Build(pkgplugins.HookContext{
			Platform:    h.platform(reg.PluginID),
			State:       state,
			ToolsBinDir: toolsBinDir,
		})
		if err == nil && item != nil {
			out = append(out, item)
		}
	}
	return out
}
