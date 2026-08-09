package controlplane

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/manifestplugins"
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
	case config.PluginKindProvider:
		if err := s.pools.ReloadPluginProviders(ctx); err != nil {
			s.log.Error("failed to reload plugin providers", "plugin", p.ID, "error", err)
		}
	}
}

// ---- manifest plugins ----

// ListManifestPlugins returns the builtin manifest overlaid with DB overrides.
func (a *Access) ListManifestPlugins(ctx context.Context) (*manifestplugins.Manifest, error) {
	return a.svc.resolveManifestPlugins(ctx)
}

// SaveManifestPlugins persists per-plugin overrides (definition and/or enabled
// toggle), re-registers the merged manifest, hot-reloads plugin tools/hooks, and
// returns the merged manifest.
func (a *Access) SaveManifestPlugins(ctx context.Context, plugins []manifestplugins.ManifestPlugin) (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	builtinByID := make(map[string]manifestplugins.ManifestPlugin, len(builtin.Plugins))
	for _, p := range builtin.Plugins {
		builtinByID[p.ID] = p
	}

	for _, plugin := range plugins {
		def, isBuiltin := builtinByID[plugin.ID]

		// Essential builtin plugins back core tools (rg/fd -> Grep/Glob); refuse to
		// disable them so an admin can't silently break the harness.
		if isBuiltin && def.Essential && !plugin.Enabled {
			return nil, invalid(fmt.Sprintf("plugin %q is essential and cannot be disabled", plugin.ID))
		}

		existing, _, err := a.svc.store.GetManifestPluginOverride(ctx, plugin.ID)
		if err != nil {
			return nil, err
		}

		// A toggle-only request sends {id, enabled} with zero-valued Kind. Don't
		// treat that as a config override — it would clobber the existing definition.
		isFullDefinition := plugin.Kind != ""

		var cfgStr string
		if isFullDefinition {
			candidate := &manifestplugins.Manifest{
				OAuthProviders: builtin.OAuthProviders,
				Plugins:        []manifestplugins.ManifestPlugin{plugin},
			}
			if err := manifestplugins.Validate(candidate); err != nil {
				return nil, invalid(fmt.Sprintf("invalid plugin %q: %v", plugin.ID, err))
			}
			// A builtin stores only the fields that differ from the definition the
			// server ships, so the rest keep following it across upgrades. An
			// admin-added plugin has nothing underneath it: the row is the plugin.
			if isBuiltin {
				cfgStr, err = manifestplugins.OverrideJSON(def, plugin)
			} else {
				cfgStr, err = manifestplugins.DefinitionJSON(plugin)
			}
			if err != nil {
				return nil, err
			}
		} else {
			cfgStr = existing.Config
		}

		enabledDiffers := isBuiltin && plugin.Enabled != def.Enabled
		var enabled *bool
		if enabledDiffers {
			e := plugin.Enabled
			enabled = &e
		}
		if !isBuiltin {
			e := plugin.Enabled
			enabled = &e
		}

		needsRow := enabled != nil || existing.SessionEnvVaultKey != "" || cfgStr != ""
		if !needsRow {
			if err := a.svc.store.DeleteManifestPluginOverride(ctx, plugin.ID); err != nil {
				return nil, err
			}
			continue
		}

		if err := a.svc.store.UpsertManifestPluginOverride(ctx, config.ManifestPluginOverride{
			PluginID:           plugin.ID,
			Enabled:            enabled,
			SessionEnvVaultKey: existing.SessionEnvVaultKey,
			Config:             cfgStr,
		}); err != nil {
			return nil, err
		}
	}

	merged, err := a.svc.resolveManifestPlugins(ctx)
	if err != nil {
		return nil, err
	}
	a.svc.plugins.RegisterManifestPlugins(merged)
	if a.svc.pools != nil {
		if err := a.svc.pools.ReloadPluginTools(ctx); err != nil {
			a.svc.log.Error("failed to reload manifest plugin tools", "error", err)
		}
		if err := a.svc.pools.ReloadPluginHooks(ctx); err != nil {
			a.svc.log.Error("failed to reload manifest plugin hooks", "error", err)
		}
	}
	return merged, nil
}

// DeleteManifestPlugin removes an admin-added plugin: its override row is the
// whole definition, so dropping it drops the plugin. A builtin ships with the
// server and is refused — disabling it is the reversible equivalent, and letting
// a delete "remove" one would only resurrect it on the next resolve. Installed
// binaries stay in the mise cache, exactly as they do when a plugin is disabled.
func (a *Access) DeleteManifestPlugin(ctx context.Context, id string) error {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return err
	}
	for _, p := range builtin.Plugins {
		if p.ID == id {
			return invalid(fmt.Sprintf("plugin %q ships with the server and cannot be removed; disable it instead", id))
		}
	}

	_, found, err := a.svc.store.GetManifestPluginOverride(ctx, id)
	if err != nil {
		return err
	}
	if !found {
		return notFound(fmt.Sprintf("plugin %q not found", id))
	}
	if err := a.svc.store.DeleteManifestPluginOverride(ctx, id); err != nil {
		return err
	}

	merged, err := a.svc.resolveManifestPlugins(ctx)
	if err != nil {
		return err
	}
	a.svc.plugins.RegisterManifestPlugins(merged)
	if a.svc.pools != nil {
		if err := a.svc.pools.ReloadPluginTools(ctx); err != nil {
			a.svc.log.Error("failed to reload manifest plugin tools", "error", err)
		}
		if err := a.svc.pools.ReloadPluginHooks(ctx); err != nil {
			a.svc.log.Error("failed to reload manifest plugin hooks", "error", err)
		}
	}
	return nil
}

// ResetManifestPlugin drops a builtin's definition override, handing the plugin
// back to the definition the running server ships. The enable switch is a
// separate decision and survives: "stop customizing this" is not "turn it on".
//
// An admin-added plugin has no definition to fall back to, so resetting one is
// refused — deleting it is the operation that means anything there.
func (a *Access) ResetManifestPlugin(ctx context.Context, id string) (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	isBuiltin := false
	for _, p := range builtin.Plugins {
		if p.ID == id {
			isBuiltin = true
			break
		}
	}
	if !isBuiltin {
		return nil, invalid(fmt.Sprintf("plugin %q has no builtin definition to reset to; remove it instead", id))
	}

	existing, found, err := a.svc.store.GetManifestPluginOverride(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found || existing.Config == "" {
		return nil, notFound(fmt.Sprintf("plugin %q is not customized", id))
	}
	existing.Config = ""
	if existing.Enabled == nil && existing.SessionEnvVaultKey == "" {
		if err := a.svc.store.DeleteManifestPluginOverride(ctx, id); err != nil {
			return nil, err
		}
	} else if err := a.svc.store.UpsertManifestPluginOverride(ctx, existing); err != nil {
		return nil, err
	}

	merged, err := a.svc.resolveManifestPlugins(ctx)
	if err != nil {
		return nil, err
	}
	a.svc.plugins.RegisterManifestPlugins(merged)
	if a.svc.pools != nil {
		if err := a.svc.pools.ReloadPluginTools(ctx); err != nil {
			a.svc.log.Error("failed to reload manifest plugin tools", "error", err)
		}
		if err := a.svc.pools.ReloadPluginHooks(ctx); err != nil {
			a.svc.log.Error("failed to reload manifest plugin hooks", "error", err)
		}
	}
	return merged, nil
}

// SyncManifestPlugins reconciles the merged manifest against the filesystem
// (installs binaries/skills) and returns the reconcile result.
func (a *Access) SyncManifestPlugins(ctx context.Context) (manifestplugins.ReconcileResult, error) {
	merged, err := a.svc.resolveManifestPlugins(ctx)
	if err != nil {
		return manifestplugins.ReconcileResult{}, err
	}
	return manifestplugins.Reconcile(ctx, merged, config.StellaHome()), nil
}

// resolveManifestPlugins loads the builtin manifest and overlays DB overrides.
// The merge rule itself belongs to manifestplugins.Resolve, which the startup
// wiring calls too — one protocol, one implementation.
func (s *Service) resolveManifestPlugins(ctx context.Context) (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	if s.store == nil {
		return builtin, nil
	}
	overrides, err := s.store.ListManifestPluginOverrides(ctx)
	if err != nil {
		return nil, err
	}
	return manifestplugins.Resolve(builtin, storedOverrides(overrides), func(id string, err error) {
		s.log.Warn("ignoring corrupt plugin override", "plugin", id, "error", err)
	}), nil
}

func storedOverrides(rows []config.ManifestPluginOverride) []manifestplugins.StoredOverride {
	out := make([]manifestplugins.StoredOverride, 0, len(rows))
	for _, ov := range rows {
		out = append(out, manifestplugins.StoredOverride{
			PluginID: ov.PluginID,
			Enabled:  ov.Enabled,
			Config:   ov.Config,
		})
	}
	return out
}
