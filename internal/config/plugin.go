package config

// Plugin kind constants.
const (
	PluginKindTool    = "tool"
	PluginKindChannel = "channel"
)

// Plugin represents a unified plugin entry stored in settings_plugins.
// IDs follow "kind/name" format, e.g. "tool/read" or "channel/telegram".
type Plugin struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"`
	Name    string         `json:"name"`
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// PluginID constructs a plugin ID from kind and name.
func PluginID(kind, name string) string { return kind + "/" + name }

// builtinToolNames lists the 5 built-in tool plugins.
var builtinToolNames = []string{"read", "bash", "edit", "write", "webfetch"}

// builtinChannelNames lists the 4 built-in channel plugins.
var builtinChannelNames = []string{"telegram", "qq", "feishu", "weixin"}

// BuiltinPluginIDs returns all 9 built-in plugin IDs in deterministic order.
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
