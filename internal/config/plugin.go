package config

// Plugin kind constants.
const (
	PluginKindTool    = "tool"
	PluginKindChannel = "channel"
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

// BuiltinPluginIDs returns all 5 built-in plugin IDs in deterministic order.
func BuiltinPluginIDs() []string {
	ids := make([]string, 0, len(builtinToolNames)+len(builtinChannelNames))
	for _, n := range builtinToolNames {
		ids = append(ids, PluginID(PluginKindTool, n))
	}
	for _, n := range builtinChannelNames {
		ids = append(ids, PluginID(PluginKindChannel, n))
	}
	return ids
}
