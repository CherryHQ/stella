package config

import "sync"

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

// BuiltinPlugin describes a code-defined plugin with its default enabled state.
type BuiltinPlugin struct {
	ID             string
	Kind           string
	Name           string
	DefaultEnabled bool
}

// BuiltinPlugins returns the authoritative list of code-defined plugins.
// DB rows in settings_plugin are optional overrides of enabled/config.
func BuiltinPlugins() []BuiltinPlugin {
	var out []BuiltinPlugin

	for _, n := range BuiltinToolNames {
		enabled := n != "mcp" && n != "webfetch"
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindTool, n), Kind: PluginKindTool, Name: n, DefaultEnabled: enabled})
	}
	for _, n := range BuiltinChannelNames {
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindChannel, n), Kind: PluginKindChannel, Name: n, DefaultEnabled: true})
	}
	for _, n := range BuiltinHookNames {
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindHook, n), Kind: PluginKindHook, Name: n, DefaultEnabled: true})
	}
	for _, n := range BuiltinMemoryNames {
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindMemory, n), Kind: PluginKindMemory, Name: n, DefaultEnabled: n != "simple"})
	}
	for _, n := range BuiltinSandboxNames {
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindSandbox, n), Kind: PluginKindSandbox, Name: n, DefaultEnabled: n == SandboxBackendLocal})
	}
	out = append(out, BuiltinPlugin{ID: "reflect", Kind: "reflect", Name: "reflect", DefaultEnabled: false})

	return out
}

// BuiltinPluginIDs returns all built-in plugin IDs in deterministic order.
// Provider instances are stored separately in settings_provider.
func BuiltinPluginIDs() []string {
	builtins := BuiltinPlugins()
	ids := make([]string, len(builtins))
	for i, b := range builtins {
		ids[i] = b.ID
	}
	return ids
}

// builtinPluginIndex returns a lookup map keyed by plugin ID, computed once.
var builtinPluginIndex = sync.OnceValue(func() map[string]BuiltinPlugin {
	builtins := BuiltinPlugins()
	m := make(map[string]BuiltinPlugin, len(builtins))
	for _, b := range builtins {
		m[b.ID] = b
	}
	return m
})

// IsBuiltinPlugin reports whether the given ID is a code-defined builtin.
func IsBuiltinPlugin(id string) bool {
	_, ok := builtinPluginIndex()[id]
	return ok
}

// BuiltinPluginByID returns the builtin definition for the given ID.
func BuiltinPluginByID(id string) (BuiltinPlugin, bool) {
	idx := builtinPluginIndex()
	b, ok := idx[id]
	return b, ok
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
