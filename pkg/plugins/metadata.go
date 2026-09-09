package plugins

import (
	"slices"
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
// distinct, typed vocabulary from the registration trait strings above.
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

// PluginInfo declares Native plugin identity, registration traits and required host ports.
// The host checks the declarations against registrations and injected services.
type PluginInfo struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind,omitempty"`
	Name         string   `json:"name,omitempty"`
	DisplayName  string   `json:"display_name,omitempty"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
	// RequiredCapabilities is the set of host Platform ports this plugin needs.
	// The host grants only these; the plugin-scoped Platform fails closed (nil)
	// on any undeclared capability. Distinct from the registration Capabilities trait.
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
