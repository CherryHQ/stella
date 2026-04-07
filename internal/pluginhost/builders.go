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
	pluginmemory "github.com/vaayne/anna/plugins/memory"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
	plugintools "github.com/vaayne/anna/plugins/tools"
)

func (h *Host) BuildEnabledTools(ctx context.Context, bc plugintools.BuildContext) []tools.Tool {
	names := plugintools.Names()
	sort.Strings(names)
	var out []tools.Tool
	for _, name := range names {
		if name == "mcp" {
			if tool := h.buildToolFromHost(ctx, "mcp", bc); tool != nil {
				out = append(out, tool)
			}
			continue
		}
		reg, ok := plugintools.Get(name)
		if !ok || reg.Required {
			continue
		}
		p, err := h.store.GetPlugin(ctx, "tool/"+name)
		if err != nil || !p.Enabled {
			continue
		}
		t, err := reg.Factory(bc)
		if err == nil && t != nil {
			out = append(out, t)
		}
	}
	return out
}

func (h *Host) buildToolFromHost(ctx context.Context, pluginID string, bc plugintools.BuildContext) tools.Tool {
	canonical := h.resolvePluginID(pluginID)
	state, err := h.DesiredState(ctx, canonical)
	if err != nil || !state.Enabled {
		return nil
	}
	h.mu.RLock()
	var reg pkgplugins.ToolRegistration
	var ok bool
	for _, candidate := range h.toolRegs {
		if candidate.PluginID == canonical {
			reg = candidate
			ok = true
			break
		}
	}
	h.mu.RUnlock()
	if !ok || reg.Build == nil {
		return nil
	}
	t, err := reg.Build(pkgplugins.ToolContext{Services: h, State: state, WorkDir: bc.WorkDir, UserDataDir: bc.UserDataDir, AnnaHome: bc.AnnaHome, Workspace: bc.Workspace, ToolsBinDir: bc.ToolsBinDir})
	if err != nil {
		return nil
	}
	return t
}

func (h *Host) BuildEnabledHooks(ctx context.Context, bc pluginhooks.BuildContext) []hooks.HookPlugin {
	names := pluginhooks.Names()
	sort.Strings(names)
	var out []hooks.HookPlugin
	for _, name := range names {
		reg, ok := pluginhooks.Get(name)
		if !ok {
			continue
		}
		p, err := h.store.GetPlugin(ctx, "hook/"+name)
		if err != nil || !p.Enabled {
			continue
		}
		item, err := reg.Factory(bc)
		if err == nil && item != nil {
			out = append(out, item)
		}
	}
	return out
}

func (h *Host) BuildProvider(name string, stateConfig map[string]any) (providers.ProviderAdapter, error) {
	reg, ok := pluginproviders.Get(name)
	if !ok {
		return nil, providers.ErrProviderNotFound
	}
	apiKey, _ := stateConfig["api_key"].(string)
	baseURL, _ := stateConfig["base_url"].(string)
	return reg.Factory(pluginproviders.ProviderConfig{APIKey: apiKey, BaseURL: baseURL})
}

func (h *Host) BuildMemory(ctx context.Context, name string, db *sql.DB, annaHome string, cfg map[string]any, summarizerFn func(context.Context, string) (string, error)) (memory.Provider, error) {
	reg, ok := pluginmemory.Get(name)
	if !ok {
		return nil, nil
	}
	return reg.Factory(ctx, pluginmemory.BuildContext{DB: db, AnnaHome: annaHome, Config: cfg, SummarizerFn: summarizerFn})
}
