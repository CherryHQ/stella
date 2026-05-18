package pluginhost

import (
	mcptool "github.com/CherryHQ/stella/internal/tools/mcp"
	notifytool "github.com/CherryHQ/stella/internal/tools/notify"
	skillstool "github.com/CherryHQ/stella/internal/tools/skills"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

// builtinRegistrations are tools that live in internal/tools/ but still
// register with the plugin host for capabilities like admin config,
// runtime management, and prompt injection.
var builtinRegistrations = []struct {
	ID       string
	Register func(pkgplugins.Host)
}{
	{mcptool.PluginID, mcptool.RegisterPlugin},
	{notifytool.PluginID, notifytool.RegisterPlugin},
	{skillstool.PluginID, skillstool.RegisterPlugin},
}

// RegisterBuiltinTools registers internal tools that use plugin host
// capabilities (admin, runtime, prompts) without going through the
// global plugin catalog.
func (h *Host) RegisterBuiltinTools() {
	for _, b := range builtinRegistrations {
		h.RegisterPluginID(b.ID)
		b.Register(h)
	}
}
