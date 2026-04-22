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

// Plugin represents a unified plugin entry stored in settings_plugins.
// IDs follow "kind/name" format, e.g. "tool/webfetch" or "channel/telegram".
type Plugin struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// PluginID constructs a plugin ID from kind and name.
func PluginID(kind, name string) string { return kind + "/" + name }

// builtinToolNames lists the built-in tool plugins.
// Core tools (read, bash, edit, write) are always-on and no longer managed as plugins.
var builtinToolNames = []string{"mcp", "mise", "tap-web", "webfetch"}

// builtinChannelNames lists the 4 built-in channel plugins.
var builtinChannelNames = []string{"telegram", "qq", "feishu", "weixin"}

// builtinHookNames lists the built-in hook plugins.
var builtinHookNames = []string{"rtk", "trace"}

// builtinProviderNames lists the built-in provider types.
var builtinProviderNames = []string{"anthropic", "openai", "openai-response"}

// builtinMemoryNames lists the built-in memory plugins.
var builtinMemoryNames = []string{"lcm", "simple"}

// builtinSandboxNames lists the built-in sandbox backend plugins.
var builtinSandboxNames = []string{SandboxBackendDocker, SandboxBackendLocal}

// builtinAuthNames lists the built-in OAuth provider plugins.
var builtinAuthNames = []string{"github", "lark"}

// builtinStandalonePlugins lists plugins that don't follow the kind/name pattern.
var builtinStandalonePlugins = []string{"reflect"}

// BuiltinPluginIDs returns all built-in plugin IDs in deterministic order.
// Provider instances are stored separately in settings_providers.
func BuiltinPluginIDs() []string {
	ids := make([]string, 0, len(builtinToolNames)+len(builtinChannelNames)+len(builtinHookNames)+len(builtinMemoryNames)+len(builtinSandboxNames)+len(builtinAuthNames)+len(builtinStandalonePlugins))
	for _, n := range builtinToolNames {
		ids = append(ids, PluginID(PluginKindTool, n))
	}
	for _, n := range builtinChannelNames {
		ids = append(ids, PluginID(PluginKindChannel, n))
	}
	for _, n := range builtinHookNames {
		ids = append(ids, PluginID(PluginKindHook, n))
	}
	for _, n := range builtinMemoryNames {
		ids = append(ids, PluginID(PluginKindMemory, n))
	}
	for _, n := range builtinSandboxNames {
		ids = append(ids, PluginID(PluginKindSandbox, n))
	}
	for _, n := range builtinAuthNames {
		ids = append(ids, PluginID(PluginKindAuth, n))
	}
	ids = append(ids, builtinStandalonePlugins...)
	return ids
}

// ActiveSandboxBackend returns the name of the enabled sandbox backend plugin,
// or SandboxBackendDocker if none is explicitly enabled.
func ActiveSandboxBackend(plugins []Plugin) string {
	for _, p := range plugins {
		if p.Kind == PluginKindSandbox && p.Enabled {
			return p.Name
		}
	}
	return SandboxBackendDocker
}
