package pluginhost

import (
	"context"
	"database/sql"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
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
		pluginID := "tool/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterTool(pkgplugins.ToolRegistration{
			PluginID:    pluginID,
			Name:        name,
			Description: name,
		})
		_ = reg
	}
	for _, name := range pluginhooks.Names() {
		reg, ok := pluginhooks.Get(name)
		if !ok {
			continue
		}
		pluginID := "hook/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterHook(pkgplugins.HookRegistration{PluginID: pluginID, Name: name})
		_ = reg
	}
	for _, name := range pluginproviders.Names() {
		reg, ok := pluginproviders.Get(name)
		if !ok {
			continue
		}
		pluginID := "provider/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterProvider(pkgplugins.ProviderRegistration{PluginID: pluginID, Name: name, Meta: pkgplugins.ProviderMeta{Name: reg.Meta.Name, DefaultURL: reg.Meta.DefaultURL}})
	}
	for _, name := range pluginmemory.List() {
		reg, ok := pluginmemory.Get(name)
		if !ok {
			continue
		}
		pluginID := "memory/" + name
		h.RegisterPluginID(pluginID)
		h.RegisterMemory(pkgplugins.MemoryRegistration{PluginID: pluginID, Name: name})
		_ = reg
	}
}
