package plugins

import "sort"

const (
	CapabilityChannel  = "channel"
	CapabilityRuntime  = "runtime"
	CapabilityConfig   = "config"
	CapabilityStatus   = "status"
	CapabilityTool     = "tool"
	CapabilityProvider = "provider"
	CapabilityHook     = "hook"
	CapabilityMemory   = "memory"
)

// PluginMeta is the minimum host discovery contract for a registered plugin.
type PluginMeta struct {
	ID                    string
	Kind                  string
	Name                  string
	DisplayName           string
	Description           string
	Managed               bool
	AdminVisible          bool
	HasConfig             bool
	HasStatus             bool
	Capabilities          []string
	SupportsNotifications bool
}

// Clone returns a shallow copy with an independent capability slice.
func (m PluginMeta) Clone() PluginMeta {
	m.Capabilities = append([]string(nil), m.Capabilities...)
	return m
}

// SortedCapabilities returns a normalized, sorted copy of the capability list.
func (m PluginMeta) SortedCapabilities() []string {
	caps := append([]string(nil), m.Capabilities...)
	sort.Strings(caps)
	return caps
}

// RegisteredPlugin is the merged discovery view of registered metadata and persisted state.
type RegisteredPlugin struct {
	Meta        PluginMeta
	State       PluginState
	Persisted   bool
	PersistedID string
}

// Clone returns a shallow copy with independent nested maps/slices.
func (p RegisteredPlugin) Clone() RegisteredPlugin {
	p.Meta = p.Meta.Clone()
	p.State = p.State.Clone()
	return p
}
