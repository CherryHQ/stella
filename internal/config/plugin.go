package config

import "sync"

// Plugin kind constants.
const (
	PluginKindTool     = "tool"
	PluginKindCLI      = "cli"
	PluginKindChannel  = "channel"
	PluginKindHook     = "hook"
	PluginKindProvider = "provider"
	PluginKindAuth     = "auth"
)

// Plugin represents a unified plugin entry stored in plugin.
// IDs follow "kind/name" format, e.g. "channel/telegram" or "provider/openai".
// ManifestPluginOverride is an override of a manifest-declared plugin.
// Both Enabled and SessionEnvVaultKey are nullable / empty-as-sentinel so the
// row can express "fallback to manifest default" without losing the row itself.
type ManifestPluginOverride struct {
	PluginID           string
	Enabled            *bool  // nil = fallback to manifest default; non-nil = override
	SessionEnvVaultKey string // empty = fallback; non-empty = vault blob with session_env override map
	Config             string // JSON manifest plugin definition override; empty = fallback to builtin
	UpdatedAt          string
}

type Plugin struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// PluginID constructs a plugin ID from kind and name.
func PluginID(kind, name string) string { return kind + "/" + name }

// BuiltinToolNames lists the built-in tool plugins.
// Core tools are reserved and managed outside the plugin system.
var BuiltinToolNames = []string{"gh", "lark-cli", "mise"}

// BuiltinChannelNames lists the built-in channel plugins.
var BuiltinChannelNames = []string{"telegram", "discord", "qq", "feishu", "dingtalk", "weixin"}

// BuiltinPlugin describes a code-defined plugin with its default enabled state.
type BuiltinPlugin struct {
	ID             string
	Kind           string
	Name           string
	DefaultEnabled bool
}

// BuiltinPlugins returns the authoritative list of code-defined plugins.
// DB rows in plugin are optional overrides of enabled/config.
func BuiltinPlugins() []BuiltinPlugin {
	var out []BuiltinPlugin

	for _, n := range BuiltinToolNames {
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindTool, n), Kind: PluginKindTool, Name: n, DefaultEnabled: true})
	}
	for _, n := range BuiltinChannelNames {
		out = append(out, BuiltinPlugin{ID: PluginID(PluginKindChannel, n), Kind: PluginKindChannel, Name: n, DefaultEnabled: false})
	}
	return out
}

// BuiltinPluginIDs returns all built-in plugin IDs in deterministic order.
// Provider instances are stored separately in provider.
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
