package server

import (
	"context"
	"net/http"
	"sort"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	coretools "github.com/CherryHQ/stella/internal/agent/sandbox"
)

const (
	agentToolSourceCore    = "core"
	agentToolSourceBuiltin = "builtin"
	agentToolSourcePlugin  = "plugin"
	agentToolSourceMCP     = "mcp"
)

func (s *Server) ListAgentTools(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	if _, code, msg := s.requireAgentAccess(ctx, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	items, err := s.agentTools(ctx, id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	writeData(w, http.StatusOK, types.AgentToolList{Tools: items})
}

func (s *Server) UpdateAgentTool(w http.ResponseWriter, r *http.Request, id string, toolName string) {
	ctx := r.Context()
	info := UserFromContext(ctx)
	if info == nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if _, code, msg := s.requireAgentManage(ctx, id); code != 0 {
		writeError(w, code, msg)
		return
	}
	if agent.IsCoreToolName(toolName) {
		writeError(w, http.StatusBadRequest, "core tools cannot be overridden")
		return
	}
	if !s.isManagedAgentTool(ctx, toolName) {
		writeError(w, http.StatusBadRequest, "tool is not managed here")
		return
	}

	var req apiserver.UpdateAgentToolJSONRequestBody
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	scope := agent.ToolOverrideScopeUserAgent
	if req.Scope != nil && *req.Scope != "" {
		scope = string(*req.Scope)
	}
	agentCtxID := ""
	if isAgentToolOverrideScope(scope) {
		agentCtxID = id
	}
	userID, agentID, ok := s.resolveScope(w, r, info, scope, agentCtxID)
	if !ok {
		return
	}

	if req.Enabled == nil {
		if err := s.toolOverrides.Clear(ctx, agent.ToolOverrideKey{
			ToolName: toolName,
			Scope:    scope,
			UserID:   userID,
			AgentID:  agentID,
		}); err != nil {
			s.writeInternalError(w, err)
			return
		}
	} else {
		if err := s.toolOverrides.Set(ctx, agent.ToolOverrideWrite{
			ToolName: toolName,
			Scope:    scope,
			UserID:   userID,
			AgentID:  agentID,
			Enabled:  *req.Enabled,
		}); err != nil {
			s.writeInternalError(w, err)
			return
		}
	}

	items, err := s.agentTools(ctx, id)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	for _, item := range items {
		if item.Name == toolName {
			writeData(w, http.StatusOK, item)
			return
		}
	}
	writeError(w, http.StatusBadRequest, "tool is not managed here")
}

func (s *Server) agentTools(ctx context.Context, agentID string) ([]types.AgentTool, error) {
	info := UserFromContext(ctx)
	if info == nil {
		return nil, nil
	}
	overrides, err := s.toolOverrides.Fetch(ctx, info.UserID, agentID)
	if err != nil {
		return nil, err
	}

	items := make([]types.AgentTool, 0)
	for _, core := range coretools.ToolDefinitionsWithAvailability() {
		def := core.Definition
		items = append(items, types.AgentTool{
			Name: def.Name, Description: def.Description,
			Source: agentToolSourceCore, Enabled: core.Available, Origin: agent.ToolOverrideOriginDefault,
			InputSchema: toolInputSchema(def.InputSchema),
		})
	}

	params := agent.RunnerParams{UserID: info.UserID, AgentID: agentID}
	for _, entry := range s.builtinTools {
		def, ok := entry.Definition()
		if !ok {
			continue
		}
		if agent.IsCoreToolName(def.Name) {
			continue
		}
		defaultEnabled := entry.Available == nil || entry.Available(ctx, params)
		decision := agent.ResolveToolOverride(defaultEnabled, def.Name, overrides)
		items = append(items, types.AgentTool{
			Name: def.Name, Description: def.Description,
			Source: agentToolSourceBuiltin, Enabled: decision.Enabled, Origin: decision.Origin,
			InputSchema: toolInputSchema(def.InputSchema),
		})
	}

	if s.pluginHost != nil {
		for _, spec := range s.pluginHost.EnabledToolSpecs(ctx) {
			if agent.IsCoreToolName(spec.Name) {
				continue
			}
			decision := agent.ResolveToolOverride(true, spec.Name, overrides)
			items = append(items, types.AgentTool{
				Name: spec.Name, Description: spec.Description,
				Source: agentToolSourcePlugin, Enabled: decision.Enabled, Origin: decision.Origin,
			})
		}
	}

	// MCP servers surface as always-enabled tools. Resolve them through the MCP
	// service's narrow context port (it already dedupes by name); when MCP is not
	// configured there are no registrations to show.
	if s.mcpSvc != nil {
		regs, err := s.mcpSvc.ResolveForContext(ctx, info.UserID, agentID)
		if err != nil {
			return nil, err
		}
		for _, reg := range regs {
			if agent.IsCoreToolName(reg.Name) {
				continue
			}
			items = append(items, types.AgentTool{
				Name: reg.Name, Description: reg.URL,
				Source: agentToolSourceMCP, Enabled: true, Origin: agent.ToolOverrideOriginDefault,
			})
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Source != items[j].Source {
			return toolSourceOrder(items[i].Source) < toolSourceOrder(items[j].Source)
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

// toolInputSchema adapts a tool definition's JSON input schema to the pointer
// shape the API type uses, returning nil for an empty schema so the field is
// omitted rather than serialized as an empty object.
func toolInputSchema(schema map[string]any) *map[string]any {
	if len(schema) == 0 {
		return nil
	}
	return &schema
}

func isAgentToolOverrideScope(scope string) bool {
	return scope == agent.ToolOverrideScopeUserAgent || scope == agent.ToolOverrideScopeSystemAgent
}

func toolSourceOrder(source string) int {
	switch source {
	case agentToolSourceCore:
		return 0
	case agentToolSourceBuiltin:
		return 1
	case agentToolSourcePlugin:
		return 2
	case agentToolSourceMCP:
		return 3
	default:
		return 4
	}
}

func (s *Server) isManagedAgentTool(ctx context.Context, name string) bool {
	for _, entry := range s.builtinTools {
		if definition, ok := entry.Definition(); ok && definition.Name == name {
			return true
		}
	}
	if s.pluginHost != nil {
		for _, spec := range s.pluginHost.EnabledToolSpecs(ctx) {
			if spec.Name == name {
				return true
			}
		}
	}
	return false
}
