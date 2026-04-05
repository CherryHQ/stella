package config

// Plugin kind constants.
const (
	PluginKindTool     = "tool"
	PluginKindChannel  = "channel"
	PluginKindHook     = "hook"
	PluginKindProvider = "provider"
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
var builtinToolNames = []string{"webfetch"}

// builtinChannelNames lists the 4 built-in channel plugins.
var builtinChannelNames = []string{"telegram", "qq", "feishu", "weixin"}

// builtinHookNames lists the built-in hook plugins.
var builtinHookNames = []string{"rtk", "trace"}

// builtinProviderNames lists the built-in provider plugins.
var builtinProviderNames = []string{"anthropic", "openai", "openai-response"}

// BuiltinPluginIDs returns all built-in plugin IDs in deterministic order.
func BuiltinPluginIDs() []string {
	ids := make([]string, 0, len(builtinToolNames)+len(builtinChannelNames)+len(builtinHookNames)+len(builtinProviderNames))
	for _, n := range builtinToolNames {
		ids = append(ids, PluginID(PluginKindTool, n))
	}
	for _, n := range builtinChannelNames {
		ids = append(ids, PluginID(PluginKindChannel, n))
	}
	for _, n := range builtinHookNames {
		ids = append(ids, PluginID(PluginKindHook, n))
	}
	for _, n := range builtinProviderNames {
		ids = append(ids, PluginID(PluginKindProvider, n))
	}
	return ids
}
