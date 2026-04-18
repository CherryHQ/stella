package plugins

import "sort"

const (
	CapabilityChannel   = "channel"
	CapabilityRuntime   = "runtime"
	CapabilityLifecycle = "lifecycle"
	CapabilityConfig    = "config"
	CapabilityStatus    = "status"
	CapabilityTool      = "tool"
	CapabilityPrompt    = "prompt"
	CapabilityProvider  = "provider"
	CapabilityHook      = "hook"
	CapabilityMemory    = "memory"
	CapabilityBinary    = "binary"
)

// PluginInfo is the host discovery metadata registered by a plugin.
// Capability traits are derived from actual registrations rather than declared here.
type PluginInfo struct {
	ID                    string   `json:"id"`
	Kind                  string   `json:"kind,omitempty"`
	Name                  string   `json:"name,omitempty"`
	DisplayName           string   `json:"display_name,omitempty"`
	Description           string   `json:"description,omitempty"`
	Managed               bool     `json:"managed,omitempty"`
	AdminVisible          bool     `json:"admin_visible,omitempty"`
	HasConfig             bool     `json:"has_config,omitempty"`
	HasStatus             bool     `json:"has_status,omitempty"`
	Capabilities          []string `json:"capabilities,omitempty"`
	SupportsNotifications bool     `json:"supports_notifications,omitempty"`
}

// Clone returns a shallow copy.
func (m PluginInfo) Clone() PluginInfo {
	m.Capabilities = append([]string(nil), m.Capabilities...)
	return m
}

// RegisteredPlugin is the merged discovery view of registered metadata and persisted state.
type RegisteredPlugin struct {
	Info                  PluginInfo
	Kind                  string
	Name                  string
	SupportsNotifications bool
	HasConfig             bool
	HasStatus             bool
	Capabilities          []string
	State                 PluginState
	Persisted             bool
	PersistedID           string
}

// Clone returns a shallow copy with independent nested maps/slices.
func (p RegisteredPlugin) Clone() RegisteredPlugin {
	p.Info = p.Info.Clone()
	p.Capabilities = append([]string(nil), p.Capabilities...)
	p.State = p.State.Clone()
	return p
}

// SortedCapabilities returns a normalized, sorted copy of the capability list.
func (p RegisteredPlugin) SortedCapabilities() []string {
	caps := append([]string(nil), p.Capabilities...)
	sort.Strings(caps)
	return caps
}
