package controlplane

import (
	"context"

	"github.com/CherryHQ/stella/internal/platform/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// ChannelPluginConfigError is the client message returned when an admin tries to
// read or write a channel plugin's config through the plugin API; channel
// instance config lives on the /channels surface instead.
const ChannelPluginConfigError = "channel instance config lives on /channels, not plugin config"

func pluginRouteID(kind, name string) string {
	if kind == name {
		return name
	}
	return config.PluginID(kind, name)
}

// ListPlugins returns the admin-visible registered plugins. The transport shapes
// each into its admin view.
func (a *Access) ListPlugins(ctx context.Context) ([]pkgplugins.RegisteredPlugin, error) {
	return a.svc.plugins.ListAdminVisiblePlugins(ctx)
}

// GetPluginStatus returns a plugin's admin status payload.
func (a *Access) GetPluginStatus(ctx context.Context, kind, name string) (any, error) {
	return a.svc.plugins.Status(ctx, pluginRouteID(kind, name))
}

// GetPluginConfig returns a plugin's stored config. Channel plugins are rejected:
// their instance config lives on /channels.
func (a *Access) GetPluginConfig(ctx context.Context, kind, name string) (map[string]any, error) {
	if kind == config.PluginKindChannel {
		return nil, invalid(ChannelPluginConfigError)
	}
	state, err := a.svc.plugins.Config().Get(ctx, pluginRouteID(kind, name))
	if err != nil {
		return nil, err
	}
	return state.Config, nil
}

// GetPluginConfigSchema returns a plugin's admin config schema.
func (a *Access) GetPluginConfigSchema(ctx context.Context, kind, name string) (map[string]any, error) {
	return a.svc.plugins.ConfigSchema(pluginRouteID(kind, name)), nil
}

// TogglePlugin enables/disables a plugin and hot-reloads its runtime.
func (a *Access) TogglePlugin(ctx context.Context, kind, name string, enabled bool) (config.Plugin, error) {
	id := pluginRouteID(kind, name)
	if err := a.svc.plugins.SetEnabled(ctx, id, enabled); err != nil {
		return config.Plugin{}, err
	}
	p, err := a.svc.store.GetPlugin(ctx, id)
	if err != nil {
		return config.Plugin{}, err
	}
	a.svc.applyAndReloadPlugin(ctx, p)
	return p, nil
}

// UpdatePluginConfig validates and persists a plugin's config, then hot-reloads
// its runtime. Channel plugins are rejected (config lives on /channels).
func (a *Access) UpdatePluginConfig(ctx context.Context, kind, name string, cfg map[string]any) (config.Plugin, error) {
	id := pluginRouteID(kind, name)
	if kind == config.PluginKindChannel {
		return config.Plugin{}, invalid(ChannelPluginConfigError)
	}
	if cfg == nil {
		cfg = map[string]any{}
	}
	if !a.svc.plugins.IsConfigurable(id) {
		return config.Plugin{}, notFound("plugin not registered: " + id)
	}
	if err := a.svc.plugins.ValidateConfig(id, cfg); err != nil {
		return config.Plugin{}, invalid("invalid request")
	}
	if err := a.svc.plugins.Config().Set(ctx, id, cfg); err != nil {
		return config.Plugin{}, err
	}
	p, err := a.svc.store.GetPlugin(ctx, id)
	if err != nil {
		return config.Plugin{}, err
	}
	a.svc.applyAndReloadPlugin(ctx, p)
	return p, nil
}

// applyAndReloadPlugin applies a plugin's runtime state and hot-reloads the pool
// so a config/enablement change takes effect without a restart.
func (s *Service) applyAndReloadPlugin(ctx context.Context, p config.Plugin) {
	if err := s.plugins.ApplyPlugin(ctx, p.ID); err != nil {
		s.log.Error("failed to apply plugin runtime", "plugin", p.ID, "error", err)
	}
	if s.pools == nil {
		return
	}
	switch p.Kind {
	case config.PluginKindTool:
		if err := s.pools.ReloadPluginTools(ctx); err != nil {
			s.log.Error("failed to reload plugin tools", "plugin", p.ID, "error", err)
		}
	case config.PluginKindHook:
		if err := s.pools.ReloadPluginHooks(ctx); err != nil {
			s.log.Error("failed to reload plugin hooks", "plugin", p.ID, "error", err)
		}
	}
}
