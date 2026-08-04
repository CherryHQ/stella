package pluginhost

import (
	"context"
	"sort"
	"strings"

	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

type ProviderType struct {
	ID         string
	Name       string
	DefaultURL string
}

func (h *Host) PluginsByKind(kind string) []string {
	metas := h.ListRegisteredPlugins()
	ids := make([]string, 0, len(metas))
	for _, meta := range metas {
		if meta.Kind == kind {
			ids = append(ids, meta.ID)
		}
	}
	return ids
}

func (h *Host) ManagedPlugins() []string {
	metas := h.ListRegisteredPlugins()
	ids := make([]string, 0, len(metas))
	for _, meta := range metas {
		if meta.Managed {
			ids = append(ids, meta.ID)
		}
	}
	return ids
}

func (h *Host) HasRuntime(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return hasRuntimeLocked(h.runtimeRegs, pluginID)
}

func (h *Host) HasConfig(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.configRegs[pluginID]
	return ok
}

func (h *Host) HasStatus(pluginID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.statusRegs[pluginID]
	return ok
}

func (h *Host) ListProviderTypes() []ProviderType {
	h.mu.RLock()
	types := make([]ProviderType, 0, len(h.providerRegs))
	for name, reg := range h.providerRegs {
		types = append(types, ProviderType{
			ID:         name,
			Name:       reg.Meta.Name,
			DefaultURL: reg.Meta.DefaultURL,
		})
	}
	h.mu.RUnlock()
	sort.Slice(types, func(i, j int) bool { return types[i].ID < types[j].ID })
	return types
}

func (h *Host) ListAdminVisiblePlugins(ctx context.Context) ([]pkgplugins.RegisteredPlugin, error) {
	registered := make(map[string]pkgplugins.RegisteredPlugin)
	for _, meta := range h.ListRegisteredPlugins() {
		if !meta.AdminVisible {
			continue
		}
		hasConfig, hasStatus, capabilities := h.derivedTraits(meta.ID)
		hasConfig, stateConfig := adminVisiblePluginConfig(meta.Kind, hasConfig, h.defaultConfigFor(meta.ID))
		registered[meta.ID] = pkgplugins.RegisteredPlugin{
			Info:         meta,
			Kind:         meta.Kind,
			Name:         meta.Name,
			HasConfig:    hasConfig,
			HasStatus:    hasStatus,
			Capabilities: capabilities,
			State:        pkgplugins.PluginState{ID: meta.ID, Enabled: false, Config: stateConfig},
		}
	}

	plugins, err := h.store.ListPlugins(ctx)
	if err != nil {
		return nil, err
	}
	overrides, err := h.store.ListPluginOverrides(ctx)
	if err != nil {
		return nil, err
	}
	persisted := make(map[string]bool, len(overrides))
	for _, plugin := range overrides {
		persisted[plugin.ID] = true
	}

	for _, plugin := range plugins {
		entry, ok := registered[plugin.ID]
		if !ok {
			entry = pkgplugins.RegisteredPlugin{
				Info:  inferredPluginMeta(plugin, plugin.ID),
				Kind:  plugin.Kind,
				Name:  plugin.Name,
				State: pkgplugins.PluginState{ID: plugin.ID, Enabled: false, Config: map[string]any{}},
			}
		}
		hasConfig, stateConfig := adminVisiblePluginConfig(entry.Kind, entry.HasConfig, cloneMap(plugin.Config))
		entry.HasConfig = hasConfig
		entry.State = pkgplugins.PluginState{ID: plugin.ID, Enabled: plugin.Enabled, Config: stateConfig}
		entry.Persisted = persisted[plugin.ID]
		if entry.Persisted || !config.IsBuiltinPlugin(plugin.ID) {
			entry.PersistedID = plugin.ID
		}
		registered[plugin.ID] = entry
	}

	out := make([]pkgplugins.RegisteredPlugin, 0, len(registered))
	for _, entry := range registered {
		out = append(out, entry.Clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Info.ID < out[j].Info.ID
	})
	return out, nil
}

func (h *Host) derivedTraits(pluginID string) (hasConfig bool, hasStatus bool, capabilities []string) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	capSet := map[string]struct{}{}
	add := func(capability string) {
		if capability == "" {
			return
		}
		capSet[capability] = struct{}{}
	}

	for _, reg := range h.toolRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityTool)
		}
	}
	for _, reg := range h.providerRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityProvider)
		}
	}
	for _, reg := range h.channelRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityChannel)
		}
	}
	for _, reg := range h.hookRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityHook)
		}
	}
	for _, reg := range h.runtimeRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityRuntime)
		}
	}
	if _, ok := h.configRegs[pluginID]; ok {
		hasConfig = true
		add(pkgplugins.CapabilityConfig)
	}
	if _, ok := h.statusRegs[pluginID]; ok {
		hasStatus = true
		add(pkgplugins.CapabilityStatus)
	}
	for _, reg := range h.promptRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityPrompt)
		}
	}
	for _, reg := range h.systemPromptRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityPrompt)
		}
	}
	for _, reg := range h.beforeRunRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityPrompt)
			add(pkgplugins.CapabilityLifecycle)
		}
	}
	for _, reg := range h.beforeToolRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityLifecycle)
		}
	}
	for _, reg := range h.afterToolRegs {
		if reg.PluginID == pluginID {
			add(pkgplugins.CapabilityLifecycle)
		}
	}
	capabilities = make([]string, 0, len(capSet))
	for capability := range capSet {
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	return hasConfig, hasStatus, capabilities
}

func (h *Host) defaultConfigFor(pluginID string) map[string]any {
	h.mu.RLock()
	reg, ok := h.configRegs[pluginID]
	h.mu.RUnlock()
	if !ok {
		return map[string]any{}
	}
	return reg.Defaults()
}

func adminVisiblePluginConfig(kind string, hasConfig bool, cfg map[string]any) (bool, map[string]any) {
	if kind == config.PluginKindChannel {
		return false, map[string]any{}
	}
	return hasConfig, cfg
}

func inferredPluginMeta(plugin config.Plugin, canonicalID string) pkgplugins.PluginInfo {
	meta := pkgplugins.PluginInfo{
		ID:           canonicalID,
		Kind:         plugin.Kind,
		Name:         plugin.Name,
		DisplayName:  plugin.Name,
		AdminVisible: true,
		HasConfig:    len(plugin.Config) > 0,
	}
	if meta.Kind == "" || meta.Name == "" {
		parts := strings.SplitN(canonicalID, "/", 2)
		if meta.Kind == "" && len(parts) == 2 {
			meta.Kind = parts[0]
		}
		if meta.Name == "" {
			if len(parts) == 2 {
				meta.Name = parts[1]
			} else {
				meta.Name = canonicalID
			}
		}
		if meta.DisplayName == "" {
			meta.DisplayName = meta.Name
		}
	}
	return meta
}
