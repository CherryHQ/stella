package host

import (
	"context"
	"sort"

	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/toolmeta"
)

// ToolMetadata returns the complete trusted Host tool registration, including
// tools currently disabled by native policy. Runner composition uses this
// snapshot both to recover ownership for enabled tools and to reserve every
// registered name against an MCP tool claiming the same exported name.
func (h *Host) ToolMetadata() []toolmeta.ActionTool {
	h.mu.RLock()
	regs := make([]pkgplugins.ToolSpec, 0, len(h.toolRegs))
	for _, reg := range h.toolRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })

	out := make([]toolmeta.ActionTool, 0, len(regs))
	for _, reg := range regs {
		metadata := toolmeta.ActionTool{Name: reg.Name, PluginID: reg.PluginID}
		if reg.PluginID != "" {
			// ToolSpec.Name is the plugin's trusted local declaration. The
			// runner never reconstructs it by parsing the exported name.
			metadata.LocalName = reg.Name
		}
		out = append(out, metadata)
	}
	return out
}

// EnabledToolSpecs returns metadata for plugin tools that are visible in the
// current server state. It does not build tools or touch runtime sandboxes.
func (h *Host) EnabledToolSpecs(ctx context.Context, agentID string) ([]pkgplugins.ToolSpec, error) {
	h.mu.RLock()
	regs := make([]pkgplugins.ToolSpec, 0, len(h.toolRegs))
	for _, reg := range h.toolRegs {
		regs = append(regs, reg)
	}
	h.mu.RUnlock()
	sort.Slice(regs, func(i, j int) bool { return regs[i].Name < regs[j].Name })

	out := make([]pkgplugins.ToolSpec, 0, len(regs))
	for _, reg := range regs {
		_, enabled, err := h.nativeState(ctx, reg.PluginID, agentID)
		if err != nil {
			return nil, err
		}
		if !enabled {
			continue
		}
		out = append(out, reg)
	}
	return out, nil
}
