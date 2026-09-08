package controlplane

import (
	"context"

	pluginapi "github.com/CherryHQ/stella/internal/plugin"
)

// UnifiedPluginManagementHandler adapts settings_plugin actions to the
// authority-bound file-backed plugin catalog.
type UnifiedPluginManagementHandler struct {
	access *pluginapi.FileAccess
}

func NewUnifiedPluginManagementHandler(access *pluginapi.FileAccess) *UnifiedPluginManagementHandler {
	return &UnifiedPluginManagementHandler{access: access}
}

func (h *UnifiedPluginManagementHandler) List(ctx context.Context, _ SettingsPluginListInput) (any, error) {
	if h == nil || h.access == nil {
		return nil, pluginapi.ErrForbidden
	}
	resources, err := h.access.List(ctx, pluginapi.ResourcePlugin, nil, "")
	if err != nil {
		return nil, err
	}
	out := make([]pluginToolView, 0, len(resources))
	for _, resource := range resources {
		out = append(out, projectFilePlugin(resource))
	}
	return map[string]any{"plugins": out, "truncated": false}, nil
}

func (h *UnifiedPluginManagementHandler) Enable(ctx context.Context, in SettingsPluginEnableInput) (any, error) {
	return h.toggle(ctx, in.PluginId, true)
}

func (h *UnifiedPluginManagementHandler) Disable(ctx context.Context, in SettingsPluginDisableInput) (any, error) {
	return h.toggle(ctx, in.PluginId, false)
}

func (h *UnifiedPluginManagementHandler) toggle(ctx context.Context, id string, enabled bool) (any, error) {
	if h == nil || h.access == nil {
		return nil, pluginapi.ErrForbidden
	}
	resource, err := h.access.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	updated, err := h.access.SetEnabled(ctx, id, resource.SettingsDigest, enabled)
	if err != nil {
		return nil, err
	}
	return projectFilePlugin(updated), nil
}

func projectFilePlugin(resource pluginapi.FileResource) pluginToolView {
	version := ""
	if resource.Package != nil {
		version = resource.Package.Manifest.Version
	}
	return pluginToolView{PluginID: resource.Key.ID(), Enabled: !resource.Disabled, Version: version}
}
