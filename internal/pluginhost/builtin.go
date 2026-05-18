package pluginhost

import (
	mcptool "github.com/CherryHQ/stella/internal/tools/mcp"
)

// RegisterBuiltinTools registers internal tools that use plugin host
// capabilities (admin config, status, runtime) without going through the
// global plugin catalog. Currently only MCP uses this for Web UI management.
func (h *Host) RegisterBuiltinTools(mcpManager *mcptool.Manager) {
	h.RegisterPluginID(mcptool.PluginID)
	mcptool.RegisterPlugin(h, mcpManager)
}
