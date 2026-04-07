package pluginhost

import (
	"context"
	"database/sql"

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

type LegacyBuildDeps struct {
	DB           *sql.DB
	AnnaHome     string
	ToolsBinDir  string
	SummarizerFn func(ctx context.Context, prompt string) (string, error)
}

func (h *Host) RegisterLegacyCapabilities(deps LegacyBuildDeps) {
	for _, name := range plugintools.Names() {
		if name == "mcp" {
			continue
		}
		reg, ok := plugintools.Get(name)
		if !ok || reg.Required {
			continue
		}
		legacyReg := reg
		pluginID := "tool/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterTool(pkgplugins.ToolRegistration{
			PluginID:    pluginID,
			Name:        name,
			Description: name,
			Required:    legacyReg.Required,
			Build: func(ctx pkgplugins.ToolContext) (tools.Tool, error) {
				return legacyReg.Factory(plugintools.BuildContext{
					WorkDir:     ctx.WorkDir,
					UserDataDir: ctx.UserDataDir,
					AnnaHome:    ctx.AnnaHome,
					Workspace:   ctx.Workspace,
					ToolsBinDir: ctx.ToolsBinDir,
				})
			},
		})
	}
	for _, name := range pluginhooks.Names() {
		reg, ok := pluginhooks.Get(name)
		if !ok {
			continue
		}
		legacyReg := reg
		pluginID := "hook/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterHook(pkgplugins.HookRegistration{
			PluginID: pluginID,
			Name:     name,
			Build: func(ctx pkgplugins.HookContext) (hooks.HookPlugin, error) {
				return legacyReg.Factory(pluginhooks.BuildContext{ToolsBinDir: ctx.ToolsBinDir})
			},
		})
	}
	for _, name := range pluginproviders.Names() {
		reg, ok := pluginproviders.Get(name)
		if !ok {
			continue
		}
		legacyReg := reg
		pluginID := "provider/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterProvider(pkgplugins.ProviderRegistration{
			PluginID: pluginID,
			Name:     name,
			Meta:     pkgplugins.ProviderMeta{Name: legacyReg.Meta.Name, DefaultURL: legacyReg.Meta.DefaultURL},
			Build: func(ctx pkgplugins.ProviderContext) (providers.ProviderAdapter, error) {
				apiKey, _ := ctx.State.Config["api_key"].(string)
				baseURL, _ := ctx.State.Config["base_url"].(string)
				return legacyReg.Factory(pluginproviders.ProviderConfig{APIKey: apiKey, BaseURL: baseURL})
			},
		})
	}
	for _, name := range pluginmemory.List() {
		reg, ok := pluginmemory.Get(name)
		if !ok {
			continue
		}
		legacyReg := reg
		pluginID := "memory/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterMemory(pkgplugins.MemoryRegistration{
			PluginID: pluginID,
			Name:     name,
			Build: func(ctx context.Context, build pkgplugins.MemoryContext) (memory.Provider, error) {
				return legacyReg.Factory(ctx, pluginmemory.BuildContext{
					DB:           build.DB,
					AnnaHome:     build.AnnaHome,
					Config:       build.State.Config,
					SummarizerFn: build.SummarizerFn,
				})
			},
		})
	}
}
