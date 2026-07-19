package pluginhost

import (
	"context"
	"sort"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// SessionPluginView returns the enabled plugin-owned session env setup used by
// runner sessions plus the registered/enabled plugin IDs prompt builders may
// need for visibility-aware output.
func (h *Host) SessionPluginView(ctx context.Context) (pkgplugins.SessionPluginView, error) {
	metas := h.ListRegisteredPlugins()
	view := pkgplugins.SessionPluginView{
		RegisteredPluginIDs: make([]string, 0, len(metas)),
	}
	registeredSet := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		view.RegisteredPluginIDs = append(view.RegisteredPluginIDs, meta.ID)
		registeredSet[meta.ID] = struct{}{}
	}

	// Add every manifest-only plugin to RegisteredPluginIDs (they are not in
	// metadataRegs). Disabled IDs remain registered so owner_plugin visibility
	// can distinguish a disabled plugin from an unrelated standalone skill.
	h.mu.RLock()
	for id := range h.manifestIDs {
		if _, exists := registeredSet[id]; !exists {
			view.RegisteredPluginIDs = append(view.RegisteredPluginIDs, id)
			registeredSet[id] = struct{}{}
		}
	}
	h.mu.RUnlock()

	sort.Strings(view.RegisteredPluginIDs)
	if h.store == nil {
		return view, nil
	}

	enabledPlugins, err := h.store.ListEnabledPlugins(ctx)
	if err != nil {
		return pkgplugins.SessionPluginView{}, err
	}
	enabledSet := make(map[string]struct{}, len(enabledPlugins))
	for _, plugin := range enabledPlugins {
		if !plugin.Enabled {
			continue
		}
		enabledSet[plugin.ID] = struct{}{}
		view.EnabledPluginIDs = append(view.EnabledPluginIDs, plugin.ID)
	}

	// Add manifest-enabled plugins to the enabled set.
	h.mu.RLock()
	for id := range h.manifestEnabledIDs {
		if _, exists := enabledSet[id]; !exists {
			enabledSet[id] = struct{}{}
			view.EnabledPluginIDs = append(view.EnabledPluginIDs, id)
		}
	}
	h.mu.RUnlock()

	sort.Strings(view.EnabledPluginIDs)

	for _, spec := range h.AllSessionEnvSpecs() {
		if _, ok := enabledSet[spec.PluginID]; ok {
			view.SessionEnvSpecs = append(view.SessionEnvSpecs, spec)
		}
	}
	sort.Slice(view.SessionEnvSpecs, func(i, j int) bool {
		if view.SessionEnvSpecs[i].EnvVar != view.SessionEnvSpecs[j].EnvVar {
			return view.SessionEnvSpecs[i].EnvVar < view.SessionEnvSpecs[j].EnvVar
		}
		return view.SessionEnvSpecs[i].PluginID < view.SessionEnvSpecs[j].PluginID
	})

	return view, nil
}
