package controlplane

import (
	"context"
	"encoding/json"
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
			configJSON := manifestPluginConfigJSON(plugin)
			if !isBuiltin || configJSON != manifestPluginConfigJSON(def) {
				candidate := &manifestplugins.Manifest{
					OAuthProviders: builtin.OAuthProviders,
					Plugins:        []manifestplugins.ManifestPlugin{plugin},
				}
				if err := manifestplugins.Validate(candidate); err != nil {
					return nil, invalid(fmt.Sprintf("invalid plugin %q: %v", plugin.ID, err))
				}
				cfgStr = configJSON
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
// Each override may carry a full plugin definition (Config JSON) plus an Enabled
// toggle: a Config replaces the entire definition; an Enabled-only override keeps
// the builtin definition with the flag toggled.
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
	byID := make(map[string]config.ManifestPluginOverride, len(overrides))
	for _, ov := range overrides {
		byID[ov.PluginID] = ov
	}
	seen := make(map[string]bool, len(builtin.Plugins))
	for i := range builtin.Plugins {
		id := builtin.Plugins[i].ID
		seen[id] = true
		ov, ok := byID[id]
		if !ok {
			continue
		}
		if ov.Config != "" {
			var p manifestplugins.ManifestPlugin
			if err := json.Unmarshal([]byte(ov.Config), &p); err != nil {
				s.log.Warn("ignoring corrupt plugin config override", "plugin", id, "error", err)
			} else {
				p.ID = id
				p.Enabled = builtin.Plugins[i].Enabled
				builtin.Plugins[i] = p
			}
		}
		if ov.Enabled != nil {
			builtin.Plugins[i].Enabled = *ov.Enabled
		}
	}
	for _, ov := range overrides {
		if seen[ov.PluginID] || ov.Config == "" {
			continue
		}
		var p manifestplugins.ManifestPlugin
		if err := json.Unmarshal([]byte(ov.Config), &p); err != nil {
			s.log.Warn("ignoring corrupt custom plugin config", "plugin", ov.PluginID, "error", err)
			continue
		}
		p.ID = ov.PluginID
		if ov.Enabled != nil {
			p.Enabled = *ov.Enabled
		}
		builtin.Plugins = append(builtin.Plugins, p)
	}
	return builtin, nil
}

// manifestPluginConfigJSON serializes a ManifestPlugin (excluding Enabled and ID)
// to a canonical JSON string for storage and comparison.
func manifestPluginConfigJSON(p manifestplugins.ManifestPlugin) string {
	type configOnly struct {
		Kind          string                               `json:"kind"`
		Name          string                               `json:"name"`
		DisplayName   string                               `json:"display_name"`
		Description   string                               `json:"description"`
		Category      string                               `json:"category,omitempty"`
		Essential     bool                                 `json:"essential,omitempty"`
		Prompt        string                               `json:"prompt,omitempty"`
		Binaries      []manifestplugins.ManifestBinary     `json:"binaries,omitempty"`
		Skills        []manifestplugins.ManifestSkill      `json:"skills,omitempty"`
		SessionEnvs   []manifestplugins.ManifestSessionEnv `json:"session_env,omitempty"`
		OAuthProvider string                               `json:"oauth_provider,omitempty"`
	}
	// Normalize empty slices to nil so omitempty produces stable JSON regardless of
	// whether the source was nil or [].
	binaries := p.Binaries
	if len(binaries) == 0 {
		binaries = nil
	}
	skills := p.Skills
	if len(skills) == 0 {
		skills = nil
	}
	sessionEnvs := p.SessionEnvs
	if len(sessionEnvs) == 0 {
		sessionEnvs = nil
	}
	data, _ := json.Marshal(configOnly{
		Kind:          p.Kind,
		Name:          p.Name,
		DisplayName:   p.DisplayName,
		Description:   p.Description,
		Category:      p.Category,
		Essential:     p.Essential,
		Prompt:        p.Prompt,
		Binaries:      binaries,
		Skills:        skills,
		SessionEnvs:   sessionEnvs,
		OAuthProvider: p.OAuthProvider,
	})
	return string(data)
}
