package config

import "log/slog"

// CLITool is an org-configured CLI tool provisioned through mise. It is stored
// as a settings_plugin row with kind=cli; the mise-specific fields live in the
// plugin's Config JSON under "mise_tool", "version", and "options".
//
// Unlike builtin tool plugins, cli plugins are purely DB-driven and per-org —
// they never appear in BuiltinPlugins(). The plugin Name doubles as the binary
// (mise shim) name exposed on PATH.
type CLITool struct {
	Name    string         // plugin Name; also the shim/binary name on PATH
	Tool    string         // mise tool key, e.g. "github:owner/repo", "npm:pkg", "uv"
	Version string         // mise version spec; empty means "latest"
	Options map[string]any // extra mise tool options, using mise.toml option names
	Enabled bool
}

// CLIToolFromPlugin decodes a kind=cli plugin row into a CLITool. The second
// return is false when the plugin is not a cli plugin or lacks a mise tool key.
func CLIToolFromPlugin(p Plugin) (CLITool, bool) {
	if p.Kind != PluginKindCLI {
		return CLITool{}, false
	}
	t := CLITool{Name: p.Name, Enabled: p.Enabled}
	if raw, present := p.Config["mise_tool"]; present {
		if v, ok := raw.(string); ok {
			t.Tool = v
		} else {
			slog.Warn("config: cli plugin mise_tool is not a string", "plugin", p.Name)
		}
	}
	if raw, present := p.Config["version"]; present {
		if v, ok := raw.(string); ok {
			t.Version = v
		} else {
			slog.Warn("config: cli plugin version is not a string", "plugin", p.Name)
		}
	}
	if raw, present := p.Config["options"]; present {
		if v, ok := raw.(map[string]any); ok {
			t.Options = v
		} else {
			slog.Warn("config: cli plugin options is not an object", "plugin", p.Name)
		}
	}
	if t.Tool == "" {
		return CLITool{}, false
	}
	return t, true
}

// CLIToolsFromPlugins maps plugin rows (typically from ListPluginsByKind with
// PluginKindCLI) to CLITool specs, skipping malformed entries.
func CLIToolsFromPlugins(plugins []Plugin) []CLITool {
	out := make([]CLITool, 0, len(plugins))
	for _, p := range plugins {
		if t, ok := CLIToolFromPlugin(p); ok {
			out = append(out, t)
		}
	}
	return out
}
