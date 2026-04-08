package pluginhost

import (
	"fmt"
	"sort"
	"strings"

	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

func (h *Host) RegisterMetadata(meta pkgplugins.PluginMeta) {
	meta = normalizeMetadata(meta)
	validateMetadataShape(meta)

	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.pluginIDs[meta.ID]; !ok {
		panic(fmt.Sprintf("pluginhost: metadata registered for unknown plugin id %q", meta.ID))
	}
	registerUnique(h.metadataRegs, meta.ID, meta, "metadata")
}

func (h *Host) ListRegisteredPlugins() []pkgplugins.PluginMeta {
	h.mu.RLock()
	metas := make([]pkgplugins.PluginMeta, 0, len(h.metadataRegs))
	for _, meta := range h.metadataRegs {
		metas = append(metas, meta.Clone())
	}
	h.mu.RUnlock()
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Kind != metas[j].Kind {
			return metas[i].Kind < metas[j].Kind
		}
		if metas[i].Name != metas[j].Name {
			return metas[i].Name < metas[j].Name
		}
		return metas[i].ID < metas[j].ID
	})
	return metas
}

func (h *Host) ValidateRegistrations() error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, meta := range h.metadataRegs {
		if meta.Managed && !hasRuntimeLocked(h.runtimeRegs, meta.ID) {
			return fmt.Errorf("pluginhost: metadata for %q declares managed runtime but no runtime is registered", meta.ID)
		}
		if meta.HasConfig {
			if _, ok := h.configRegs[meta.ID]; !ok {
				return fmt.Errorf("pluginhost: metadata for %q declares config but no config is registered", meta.ID)
			}
		}
		if meta.HasStatus {
			if _, ok := h.statusRegs[meta.ID]; !ok {
				return fmt.Errorf("pluginhost: metadata for %q declares status but no status is registered", meta.ID)
			}
		}
		for _, capability := range meta.Capabilities {
			switch capability {
			case pkgplugins.CapabilityChannel:
				if !hasChannelLocked(h.channelRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares channel capability but no channel is registered", meta.ID)
				}
			case pkgplugins.CapabilityLifecycle:
				if !hasLifecycleLocked(h.beforeRunRegs, h.beforeToolRegs, h.afterToolRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares lifecycle capability but no lifecycle hook is registered", meta.ID)
				}
			case pkgplugins.CapabilityRuntime:
				if !hasRuntimeLocked(h.runtimeRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares runtime capability but no runtime is registered", meta.ID)
				}
			case pkgplugins.CapabilityConfig:
				if _, ok := h.configRegs[meta.ID]; !ok {
					return fmt.Errorf("pluginhost: metadata for %q declares config capability but no config is registered", meta.ID)
				}
			case pkgplugins.CapabilityStatus:
				if _, ok := h.statusRegs[meta.ID]; !ok {
					return fmt.Errorf("pluginhost: metadata for %q declares status capability but no status is registered", meta.ID)
				}
			case pkgplugins.CapabilityTool:
				if !hasToolLocked(h.toolRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares tool capability but no tool is registered", meta.ID)
				}
			case pkgplugins.CapabilityPrompt:
				if !hasPromptLocked(h.promptRegs, h.systemPromptRegs, h.beforeRunRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares prompt capability but no prompt contribution is registered", meta.ID)
				}
			case pkgplugins.CapabilityProvider:
				if !hasProviderLocked(h.providerRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares provider capability but no provider is registered", meta.ID)
				}
			case pkgplugins.CapabilityHook:
				if !hasHookLocked(h.hookRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares hook capability but no hook is registered", meta.ID)
				}
			case pkgplugins.CapabilityMemory:
				if !hasMemoryLocked(h.memoryRegs, meta.ID) {
					return fmt.Errorf("pluginhost: metadata for %q declares memory capability but no memory is registered", meta.ID)
				}
			}
		}
	}
	return nil
}

func normalizeMetadata(meta pkgplugins.PluginMeta) pkgplugins.PluginMeta {
	meta = meta.Clone()
	seen := make(map[string]struct{}, len(meta.Capabilities))
	caps := make([]string, 0, len(meta.Capabilities))
	for _, capability := range meta.Capabilities {
		capability = strings.TrimSpace(capability)
		if capability == "" {
			panic("pluginhost: empty metadata capability")
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		caps = append(caps, capability)
	}
	sort.Strings(caps)
	meta.Capabilities = caps
	return meta
}

func validateMetadataShape(meta pkgplugins.PluginMeta) {
	if meta.ID == "" {
		panic("pluginhost: metadata missing plugin id")
	}
	if meta.Kind == "" {
		panic(fmt.Sprintf("pluginhost: metadata for %q missing kind", meta.ID))
	}
	if meta.Name == "" {
		panic(fmt.Sprintf("pluginhost: metadata for %q missing name", meta.ID))
	}
	if meta.DisplayName == "" {
		panic(fmt.Sprintf("pluginhost: metadata for %q missing display name", meta.ID))
	}
}

func hasRuntimeLocked(regs map[string]pkgplugins.RuntimeRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasToolLocked(regs map[string]pkgplugins.ToolRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasProviderLocked(regs map[string]pkgplugins.ProviderRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasChannelLocked(regs map[string]pkgplugins.ChannelRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasHookLocked(regs map[string]pkgplugins.HookRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasBeforeRunLocked(regs map[string]pkgplugins.BeforeRunRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasBeforeToolLocked(regs map[string]pkgplugins.BeforeToolCallRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasAfterToolLocked(regs map[string]pkgplugins.AfterToolResultRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasLifecycleLocked(beforeRunRegs map[string]pkgplugins.BeforeRunRegistration, beforeToolRegs map[string]pkgplugins.BeforeToolCallRegistration, afterToolRegs map[string]pkgplugins.AfterToolResultRegistration, pluginID string) bool {
	return hasBeforeRunLocked(beforeRunRegs, pluginID) || hasBeforeToolLocked(beforeToolRegs, pluginID) || hasAfterToolLocked(afterToolRegs, pluginID)
}

func hasMemoryLocked(regs map[string]pkgplugins.MemoryRegistration, pluginID string) bool {
	for _, reg := range regs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}

func hasPromptLocked(promptRegs map[string]pkgplugins.PromptInventoryRegistration, systemRegs map[string]pkgplugins.SystemPromptRegistration, beforeRunRegs map[string]pkgplugins.BeforeRunRegistration, pluginID string) bool {
	for _, reg := range promptRegs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	for _, reg := range systemRegs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	for _, reg := range beforeRunRegs {
		if reg.PluginID == pluginID {
			return true
		}
	}
	return false
}
