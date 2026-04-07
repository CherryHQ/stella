package pluginhost

import (
	"context"
	"sort"
	"strings"

	"github.com/vaayne/anna/internal/config"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
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
		registered[meta.ID] = pkgplugins.RegisteredPlugin{
			Meta:  meta,
			State: pkgplugins.PluginState{ID: meta.ID, Enabled: false, Config: h.defaultConfigFor(meta.ID)},
		}
	}

	persisted, err := h.store.ListPlugins(ctx)
	if err != nil {
		return nil, err
	}
	for _, plugin := range persisted {
		entry, ok := registered[plugin.ID]
		if !ok {
			entry = pkgplugins.RegisteredPlugin{
				Meta:  inferredPluginMeta(plugin, plugin.ID),
				State: pkgplugins.PluginState{ID: plugin.ID, Enabled: false, Config: map[string]any{}},
			}
		}
		entry.State = pkgplugins.PluginState{ID: plugin.ID, Enabled: plugin.Enabled, Config: cloneMap(plugin.Config)}
		entry.Persisted = true
		entry.PersistedID = plugin.ID
		registered[plugin.ID] = entry
	}

	out := make([]pkgplugins.RegisteredPlugin, 0, len(registered))
	for _, entry := range registered {
		out = append(out, entry.Clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Meta.Kind != out[j].Meta.Kind {
			return out[i].Meta.Kind < out[j].Meta.Kind
		}
		if out[i].Meta.Name != out[j].Meta.Name {
			return out[i].Meta.Name < out[j].Meta.Name
		}
		return out[i].Meta.ID < out[j].Meta.ID
	})
	return out, nil
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

func inferredPluginMeta(plugin config.Plugin, canonicalID string) pkgplugins.PluginMeta {
	meta := pkgplugins.PluginMeta{
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
