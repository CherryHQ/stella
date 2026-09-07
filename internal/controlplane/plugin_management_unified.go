package controlplane

import (
	"context"
	"fmt"

	pluginapi "github.com/CherryHQ/stella/internal/plugin"
)

// UnifiedPluginManagementHandler adapts settings_plugin actions to the
// authority-bound unified plugin catalog.
type UnifiedPluginManagementHandler struct {
	access *pluginapi.Access
}

// NewUnifiedPluginManagementHandler binds the generated settings_plugin
// actions to an authority-bound unified plugin Access. The system-scope
// queries and mutations below retain Access's normal admin authorization.
func NewUnifiedPluginManagementHandler(access *pluginapi.Access) *UnifiedPluginManagementHandler {
	return &UnifiedPluginManagementHandler{access: access}
}

func (h *UnifiedPluginManagementHandler) List(ctx context.Context, _ SettingsPluginListInput) (any, error) {
	if h == nil || h.access == nil {
		return nil, pluginapi.ErrForbidden
	}
	definitions, err := h.access.ListDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]pluginToolView, 0, len(definitions))
	for _, definition := range definitions {
		configs, err := h.access.ListConfigs(ctx, definition.ID, pluginapi.ScopeSystem, "")
		if err != nil {
			return nil, err
		}
		enabled, err := systemPluginEnabled(definition, configs)
		if err != nil {
			return nil, err
		}
		out = append(out, projectPlugin(definition.ID, enabled))
	}
	return map[string]any{"plugins": out, "truncated": false}, nil
}

func (h *UnifiedPluginManagementHandler) Enable(ctx context.Context, in SettingsPluginEnableInput) (any, error) {
	return h.toggle(ctx, in.PluginId, true)
}

func (h *UnifiedPluginManagementHandler) Disable(ctx context.Context, in SettingsPluginDisableInput) (any, error) {
	return h.toggle(ctx, in.PluginId, false)
}

func (h *UnifiedPluginManagementHandler) toggle(ctx context.Context, pluginID string, enabled bool) (any, error) {
	if h == nil || h.access == nil {
		return nil, pluginapi.ErrForbidden
	}
	definition, err := h.access.GetDefinition(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	configs, err := h.access.ListConfigs(ctx, pluginID, pluginapi.ScopeSystem, "")
	if err != nil {
		return nil, err
	}
	if len(configs) == 0 {
		return nil, fmt.Errorf("%w: system config missing for plugin %q", pluginapi.ErrNotFound, pluginID)
	}
	if len(configs) > 1 {
		return nil, fmt.Errorf("%w: multiple system configs for plugin %q", pluginapi.ErrConflict, pluginID)
	}
	updated, err := h.access.UpdateConfig(ctx, pluginID, configs[0].ID, configs[0].Revision, pluginapi.ConfigPatch{
		EnabledSet: true,
		Enabled:    &enabled,
	})
	if err != nil {
		return nil, err
	}
	return projectPlugin(definition.ID, enabledValue(definition, updated)), nil
}

// systemPluginEnabled projects the existing system config, with an absent
// config inheriting the definition default. SyncBuiltinDefaults creates rows
// for shipped definitions; custom definitions remain visible while their
// system state is absent until a complete config is created.
func systemPluginEnabled(definition pluginapi.Definition, configs []pluginapi.Config) (bool, error) {
	if len(configs) > 1 {
		return false, fmt.Errorf("%w: multiple system configs for plugin %q", pluginapi.ErrConflict, definition.ID)
	}
	if len(configs) == 0 || configs[0].Enabled == nil {
		return definition.DefaultEnabled, nil
	}
	return *configs[0].Enabled, nil
}

func enabledValue(definition pluginapi.Definition, config pluginapi.Config) bool {
	if config.Enabled == nil {
		return definition.DefaultEnabled
	}
	return *config.Enabled
}
