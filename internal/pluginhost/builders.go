package pluginhost

import (
	"context"
	"database/sql"
	"sort"

	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/memory"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	pluginhooks "github.com/vaayne/anna/plugins/hooks"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func (h *Host) BuildEnabledTools(ctx context.Context, bc plugintools.BuildContext) []tools.Tool {
	h.mu.RLock()
	regs := make([]pkgplugins.ToolRegistration, 0, len(h.toolRegs))
	for _, reg := range h.toolRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })
	var out []tools.Tool
	for _, reg := range regs {
		if reg.Required {
			continue
		}
		state, err := h.DesiredState(ctx, reg.PluginID)
		if err != nil || !state.Enabled || reg.Build == nil {
			continue
		}
		t, err := reg.Build(pkgplugins.ToolContext{
			Services:    h,
			State:       state,
			WorkDir:     bc.WorkDir,
			UserDataDir: bc.UserDataDir,
			AnnaHome:    bc.AnnaHome,
			Workspace:   bc.Workspace,
			ToolsBinDir: bc.ToolsBinDir,
		})
		if err == nil && t != nil {
			out = append(out, t)
		}
	}
	return out
}

func (h *Host) BuildCoreTools(bc plugintools.BuildContext) []tools.Tool {
	h.mu.RLock()
	regs := make([]pkgplugins.ToolRegistration, 0, len(h.toolRegs))
	for _, reg := range h.toolRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })
	var out []tools.Tool
	for _, reg := range regs {
		if !reg.Required || reg.Build == nil {
			continue
		}
		t, err := reg.Build(pkgplugins.ToolContext{
			Services: h,
			State: pkgplugins.PluginState{
				ID:      reg.PluginID,
				Enabled: true,
				Config:  h.defaultConfigFor(reg.PluginID),
			},
			WorkDir:     bc.WorkDir,
			UserDataDir: bc.UserDataDir,
			AnnaHome:    bc.AnnaHome,
			Workspace:   bc.Workspace,
			ToolsBinDir: bc.ToolsBinDir,
		})
		if err == nil && t != nil {
			out = append(out, t)
		}
	}
	return out
}

func (h *Host) BuildEnabledHooks(ctx context.Context, bc pluginhooks.BuildContext) []hooks.HookPlugin {
	h.mu.RLock()
	regs := make([]pkgplugins.HookRegistration, 0, len(h.hookRegs))
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
			Services:    h,
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
		Services: h,
		State: pkgplugins.PluginState{
			ID:      reg.PluginID,
			Enabled: true,
			Config:  cloneMap(stateConfig),
		},
	})
}

func (h *Host) BuildProviderRegistry(name string, stateConfig map[string]any) (*providers.Registry, error) {
	adapter, err := h.BuildProvider(name, stateConfig)
	if err != nil {
		return nil, err
	}
	reg := providers.NewRegistry()
	reg.Register(adapter)
	return reg, nil
}

func (h *Host) BuildMemory(ctx context.Context, name string, db *sql.DB, annaHome string, cfg map[string]any, summarizerFn func(context.Context, string) (string, error)) (memory.Provider, error) {
	h.mu.RLock()
	reg, ok := h.memoryRegs[name]
	h.mu.RUnlock()
	if !ok || reg.Build == nil {
		return nil, nil
	}
	return reg.Build(ctx, pkgplugins.MemoryContext{
		Services:     h,
		State:        pkgplugins.PluginState{ID: reg.PluginID, Enabled: true, Config: cloneMap(cfg)},
		DB:           db,
		AnnaHome:     annaHome,
		SummarizerFn: summarizerFn,
	})
}
