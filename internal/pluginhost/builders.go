package pluginhost

import (
	"context"
	"sort"

	"github.com/CherryHQ/stella/pkg/hooks"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/tools"
	pluginhooks "github.com/CherryHQ/stella/plugins/hooks"
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

func (h *Host) BuildEnabledHooks(ctx context.Context, bc pluginhooks.BuildContext) []hooks.HookPlugin {
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
			ToolsBinDir: bc.ToolsBinDir,
		})
		if err == nil && item != nil {
			out = append(out, item)
		}
	}
	return out
}

func (h *Host) BuildProvider(name string, stateConfig map[string]any) (providers.ProviderAdapter, error) {
	h.mu.RLock()
	reg, ok := h.providerRegs[name]
	h.mu.RUnlock()
	if !ok || reg.Build == nil {
		return nil, providers.ErrProviderNotFound
	}
	return reg.Build(pkgplugins.ProviderContext{
		Platform: h.platform(reg.PluginID),
		State: pkgplugins.PluginState{
			ID:      reg.PluginID,
			Enabled: true,
			Config:  cloneMap(stateConfig),
		},
	})
}

func (h *Host) BuildStreamFunc(name string, stateConfig map[string]any) (providers.StreamFunc, error) {
	adapter, err := h.BuildProvider(name, stateConfig)
	if err != nil {
		return nil, err
	}
	return providers.AdapterStreamFunc(adapter), nil
}
