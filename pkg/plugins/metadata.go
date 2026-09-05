package plugins

import (
	"slices"
	"sort"
)

const (
	CapabilityChannel   = "channel"
	CapabilityRuntime   = "runtime"
	CapabilityLifecycle = "lifecycle"
	CapabilityConfig    = "config"
	CapabilityStatus    = "status"
	CapabilityTool      = "tool"
	CapabilityPrompt    = "prompt"
	CapabilityHook      = "hook"
)

// Capability identifies a single host service exposed through the plugin-scoped
// Platform. A plugin must DECLARE the capabilities it needs in
// PluginInfo.RequiredCapabilities; the host grants only those, and a Platform
// accessor for an undeclared capability returns nil (fail-closed). This is a
// distinct, typed vocabulary from the derived trait strings above (which report
// what a plugin registered, not what host ports it may reach).
type Capability string

const (
	CapabilityLogger            Capability = "logger"
	CapabilityConfigStore       Capability = "config_store"
	CapabilityStateStore        Capability = "state_store"
	CapabilityNotifier          Capability = "notifier"
	CapabilityAuth              Capability = "auth"
	CapabilityRuntimeLookup     Capability = "runtime_lookup"
	CapabilityChannelPlatform   Capability = "channel_platform"
	CapabilityAccountEnrollment Capability = "account_enrollment"
)

// PluginInfo is the host discovery metadata registered by a plugin.
// Capability traits are derived from actual registrations rather than declared here.
type PluginInfo struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind,omitempty"`
	Name         string   `json:"name,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	Description  string   `json:"description,omitempty"`
	Managed      bool     `json:"managed,omitempty"`
	AdminVisible bool     `json:"admin_visible,omitempty"`
	HasConfig    bool     `json:"has_config,omitempty"`
	HasStatus    bool     `json:"has_status,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// RequiredCapabilities is the set of host Platform ports this plugin needs.
	// The host grants only these; the plugin-scoped Platform fails closed (nil)
	// on any undeclared capability. Distinct from the derived Capabilities trait.
	RequiredCapabilities []Capability `json:"required_capabilities,omitempty"`
}

// Clone returns a shallow copy.
func (m PluginInfo) Clone() PluginInfo {
	m.Capabilities = append([]string(nil), m.Capabilities...)
	m.RequiredCapabilities = append([]Capability(nil), m.RequiredCapabilities...)
	return m
}

// RequiresCapability reports whether the plugin declared the given capability.
func (m PluginInfo) RequiresCapability(c Capability) bool {
	return slices.Contains(m.RequiredCapabilities, c)
}

// RegisteredPlugin is the merged discovery view of registered metadata and persisted state.
type RegisteredPlugin struct {
	Info         PluginInfo
	Kind         string
	Name         string
	HasConfig    bool
	HasStatus    bool
	Capabilities []string
	State        PluginState
	Persisted    bool
	PersistedID  string
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
