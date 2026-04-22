package pluginhost

import (
	"context"
	"sort"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

// SessionPluginView returns the enabled plugin-owned wrapper/env setup used by
// runner sessions plus the registered/enabled plugin IDs prompt builders may
// need for visibility-aware output.
func (h *Host) SessionPluginView(ctx context.Context) (pkgplugins.SessionPluginView, error) {
	metas := h.ListRegisteredPlugins()
	view := pkgplugins.SessionPluginView{
		RegisteredPluginIDs: make([]string, 0, len(metas)),
	}
	for _, meta := range metas {
		view.RegisteredPluginIDs = append(view.RegisteredPluginIDs, meta.ID)
	}
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
	sort.Strings(view.EnabledPluginIDs)

	for _, spec := range h.AllWrapperSpecs() {
		if _, ok := enabledSet[spec.PluginID]; ok {
			view.WrapperSpecs = append(view.WrapperSpecs, spec)
		}
	}
	sort.Slice(view.WrapperSpecs, func(i, j int) bool {
		if view.WrapperSpecs[i].Name != view.WrapperSpecs[j].Name {
			return view.WrapperSpecs[i].Name < view.WrapperSpecs[j].Name
		}
		return view.WrapperSpecs[i].PluginID < view.WrapperSpecs[j].PluginID
	})

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
