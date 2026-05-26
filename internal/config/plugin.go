package config

// Plugin kind constants.
const (
	PluginKindTool     = "tool"
	PluginKindChannel  = "channel"
	PluginKindHook     = "hook"
	PluginKindProvider = "provider"
	PluginKindMemory   = "memory"
	PluginKindSandbox  = "sandbox"
	PluginKindAuth     = "auth"
)

// Plugin represents a unified plugin entry stored in settings_plugin.
// IDs follow "kind/name" format, e.g. "tool/webfetch" or "channel/telegram".
type Plugin struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
	OrgID   string         `json:"org_id,omitempty"`
}

// PluginID constructs a plugin ID from kind and name.
func PluginID(kind, name string) string { return kind + "/" + name }

// BuiltinToolNames lists the built-in tool plugins.
// Core tools (read, bash, edit, write) are always-on and no longer managed as plugins.
var BuiltinToolNames = []string{"gh", "lark-cli", "mcp", "mise", "tap-web", "webfetch"}

// BuiltinChannelNames lists the 4 built-in channel plugins.
var BuiltinChannelNames = []string{"telegram", "qq", "feishu", "weixin"}

// BuiltinHookNames lists the built-in hook plugins.
var BuiltinHookNames = []string{"rtk", "trace"}

// BuiltinProviderNames lists the built-in provider types.
var BuiltinProviderNames = []string{"anthropic", "openai", "openai-response"}

// BuiltinMemoryNames lists the built-in memory plugins.
var BuiltinMemoryNames = []string{"lcm", "simple"}

// BuiltinSandboxNames lists the built-in sandbox backend plugins.
var BuiltinSandboxNames = []string{SandboxBackendDocker, SandboxBackendLocal, SandboxBackendNone}

// BuiltinStandalonePlugins lists plugins that don't follow the kind/name pattern.
var BuiltinStandalonePlugins = []string{"reflect"}

// BuiltinPluginIDs returns all built-in plugin IDs in deterministic order.
// Provider instances are stored separately in settings_provider.
func BuiltinPluginIDs() []string {
	ids := make([]string, 0, len(BuiltinToolNames)+len(BuiltinChannelNames)+len(BuiltinHookNames)+len(BuiltinMemoryNames)+len(BuiltinSandboxNames)+len(BuiltinStandalonePlugins))
	for _, n := range BuiltinToolNames {
		ids = append(ids, PluginID(PluginKindTool, n))
	}
	for _, n := range BuiltinChannelNames {
		ids = append(ids, PluginID(PluginKindChannel, n))
	}
	for _, n := range BuiltinHookNames {
		ids = append(ids, PluginID(PluginKindHook, n))
	}
	for _, n := range BuiltinMemoryNames {
		ids = append(ids, PluginID(PluginKindMemory, n))
	}
	for _, n := range BuiltinSandboxNames {
		ids = append(ids, PluginID(PluginKindSandbox, n))
	}
	ids = append(ids, BuiltinStandalonePlugins...)
	return ids
}

// ActiveSandboxBackend returns the name of the enabled sandbox backend plugin,
// or SandboxBackendLocal if none is explicitly enabled.
func ActiveSandboxBackend(plugins []Plugin) string {
	for _, p := range plugins {
		if p.Kind == PluginKindSandbox && p.Enabled {
			return p.Name
		}
	}
	return SandboxBackendLocal
}
