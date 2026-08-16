package controlplane

import (
	"context"
	"errors"
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

// Every manifest mutation addresses exactly one plugin.
//
// They used to share one endpoint that took the whole plugin list and rewrote
// every row from it, which made a one-plugin toggle a deployment-wide write: a
// stale tab could overwrite an edit it never saw, and toggling any plugin
// converted every legacy row it happened to be carrying. Addressing one plugin
// is also what lets ownership be recorded instead of recomputed — a request now
// says which fields it is taking, and the rest of the row survives untouched.

// SetManifestPluginEnabled turns one plugin on or off. The enable switch is its
// own column, so this never touches the definition override: "turn this off" and
// "stop customizing this" stay independent decisions.
func (a *Access) SetManifestPluginEnabled(ctx context.Context, id string, enabled bool) (*manifestplugins.Manifest, error) {
	def, isBuiltin, err := a.builtinDefinition(id)
	if err != nil {
		return nil, err
	}
	existing, found, err := a.svc.store.GetManifestPluginOverride(ctx, id)
	if err != nil {
		return nil, err
	}
	if !isBuiltin && !found {
		return nil, notFound(fmt.Sprintf("plugin %q not found", id))
	}
	// Essential builtin plugins back core tools (rg/fd -> Grep/Glob); refuse to
	// disable them so an admin can't silently break the harness.
	if isBuiltin && def.Essential && !enabled {
		return nil, invalid(fmt.Sprintf("plugin %q is essential and cannot be disabled", id))
	}

	// A builtin agreeing with the shipped default needs no stored answer; an
	// admin-added plugin has no default to agree with, so its row always carries
	// one.
	var stored *bool
	if !isBuiltin || enabled != def.Enabled {
		e := enabled
		stored = &e
	}
	existing.PluginID = id
	existing.Enabled = stored
	return a.writeOverride(ctx, existing)
}

// SaveManifestPluginDefinition records the fields this request takes ownership
// of and leaves every other field of the row alone.
//
// fields is the contract: a builtin's row grows exactly those keys, at the
// submitted values, on top of whatever it already owned. Deriving them here by
// diffing against the shipped definition is what this replaces — that quietly
// released a pinned field whenever a release happened to ship the same value.
//
// An admin-added plugin has no definition underneath it, so its row is the whole
// plugin and fields does not apply; its enable state travels with it, because
// there is no shipped default to fall back to.
func (a *Access) SaveManifestPluginDefinition(ctx context.Context, plugin manifestplugins.ManifestPlugin, fields []string) (*manifestplugins.Manifest, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return nil, err
	}
	candidate := &manifestplugins.Manifest{
		OAuthProviders: builtin.OAuthProviders,
		Plugins:        []manifestplugins.ManifestPlugin{plugin},
	}
	if err := manifestplugins.Validate(candidate); err != nil {
		return nil, invalid(fmt.Sprintf("invalid plugin %q: %v", plugin.ID, err))
	}

	_, isBuiltin, err := a.builtinDefinition(plugin.ID)
	if err != nil {
		return nil, err
	}
	existing, _, err := a.svc.store.GetManifestPluginOverride(ctx, plugin.ID)
	if err != nil {
		return nil, err
	}
	existing.PluginID = plugin.ID

	if !isBuiltin {
		cfg, err := manifestplugins.DefinitionJSON(plugin)
		if err != nil {
			return nil, err
		}
		enabled := plugin.Enabled
		existing.Config = cfg
		existing.Enabled = &enabled
		return a.writeOverride(ctx, existing)
	}

	cfg, err := manifestplugins.SetFields(existing.Config, plugin, fields)
	if err != nil {
		return nil, invalid(err.Error())
	}
	existing.Config = cfg
	return a.writeOverride(ctx, existing)
}

// builtinDefinition reports the shipped definition behind an ID, if there is one.
func (a *Access) builtinDefinition(id string) (manifestplugins.ManifestPlugin, bool, error) {
	builtin, err := manifestplugins.LoadBuiltin()
	if err != nil {
		return manifestplugins.ManifestPlugin{}, false, err
	}
	for _, p := range builtin.Plugins {
		if p.ID == id {
			return p, true, nil
		}
	}
	return manifestplugins.ManifestPlugin{}, false, nil
}

// writeOverride persists one row — dropping it when it no longer says anything —
// then re-resolves, re-registers, and hot-reloads.
func (a *Access) writeOverride(ctx context.Context, row config.ManifestPluginOverride) (*manifestplugins.Manifest, error) {
	if row.Enabled == nil && row.Config == "" && row.SessionEnvVaultKey == "" {
		if err := a.svc.store.DeleteManifestPluginOverride(ctx, row.PluginID); err != nil {
			return nil, err
		}
	} else if err := a.svc.store.UpsertManifestPluginOverride(ctx, row); err != nil {
		return nil, err
	}
	return a.reloadManifest(ctx)
}

func (a *Access) reloadManifest(ctx context.Context) (*manifestplugins.Manifest, error) {
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

// ResetManifestPlugin hands a builtin's definition back to the server, either
// one field of it or all of it. The enable switch is a separate decision and
// survives either way: "stop customizing this" is not "turn it on".
//
// Releasing one field from a row written before overrides went sparse converts
// it first. That conversion is lossless — such a row owns every field, including
// the ones its JSON omits and therefore owns as empty — so the other fields stay
// pinned exactly where they were, and only the named one starts following the
// server again.
//
// An admin-added plugin has no definition to fall back to, so resetting one is
// refused — deleting it is the operation that means anything there.
func (a *Access) ResetManifestPlugin(ctx context.Context, id, field string) (*manifestplugins.Manifest, error) {
	_, isBuiltin, err := a.builtinDefinition(id)
	if err != nil {
		return nil, err
	}
	if !isBuiltin {
		return nil, invalid(fmt.Sprintf("plugin %q has no builtin definition to reset to; remove it instead", id))
	}
	if field != "" && !manifestplugins.IsOwnableField(field) {
		return nil, invalid(fmt.Sprintf("%q is not a definition field", field))
	}

	existing, found, err := a.svc.store.GetManifestPluginOverride(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found || existing.Config == "" {
		return nil, notFound(fmt.Sprintf("plugin %q is not customized", id))
	}

	if field == "" {
		existing.Config = ""
	} else {
		cfg, err := manifestplugins.ReleaseField(existing.Config, field)
		if err != nil {
			if errors.Is(err, manifestplugins.ErrFieldNotOwned) {
				return nil, notFound(fmt.Sprintf("plugin %q does not override %q", id, field))
			}
			return nil, err
		}
		existing.Config = cfg
	}
	return a.writeOverride(ctx, existing)
}

// SyncManifestPlugins reconciles the merged manifest against the filesystem
// (installs binaries/skills) and returns the reconcile result.
func (a *Access) SyncManifestPlugins(ctx context.Context) (manifestplugins.ReconcileResult, error) {
	if err := a.svc.admitHome(ctx); err != nil {
		return manifestplugins.ReconcileResult{}, err
	}
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
