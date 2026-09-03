package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	coretools "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/mcp"
)

const (
	agentToolSourceCore    = "core"
	agentToolSourceBuiltin = "builtin"
	agentToolSourcePlugin  = "plugin"
	agentToolSourceMCP     = "mcp"

	agentToolControlOverride = "override"
	agentToolControlSystem   = "system"

	agentToolReasonCoreSandbox        = "core_sandbox"
	agentToolReasonSettingsPolicy     = "settings_policy"
	agentToolReasonRuntimeUnavailable = "runtime_unavailable"

	agentToolFamilyCore   = "core_tools"
	agentToolFamilyPlugin = "plugin_tools"
	agentToolFamilyOther  = "other_tools"
)

func (s *Server) ListAgentTools(w http.ResponseWriter, r *http.Request, id string) {
	ctx := r.Context()
	agentRow, code, msg := s.requireAgentAccess(ctx, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	// Settings policy metadata is owner-managed configuration. Reuse the Agent
	// PEP instead of deriving ownership from a client-visible creator id.
	canManage := false
	if _, manageCode, manageMsg := s.requireAgentManage(ctx, id); manageCode == 0 {
		canManage = true
	} else if manageCode >= http.StatusInternalServerError {
		writeError(w, manageCode, manageMsg)
		return
	}
	items, err := s.agentTools(ctx, id, canManage && agentRow.SystemSettingsToolsEnabled)
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
	managedAgent, code, msg := s.requireAgentManage(ctx, id)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	if agent.IsCoreToolName(toolName) {
		writeError(w, http.StatusBadRequest, "core sandbox tools are system-managed")
		return
	}
	if _, isSettingsAction := settingspolicy.Lookup(toolName); isSettingsAction {
		// A profile request is not a trusted foreground session. Settings actions
		// are catalogued separately and runner availability always wins, so this
		// endpoint must not persist an override that can never register the tool.
		writeError(w, http.StatusBadRequest, "system settings are policy-managed")
		return
	}
	overridable, err := s.agentToolOverrideAllowed(ctx, info.UserID, id, toolName)
	if err != nil {
		s.writeInternalError(w, err)
		return
	}
	if !overridable {
		writeError(w, http.StatusBadRequest, "tool is not currently registered for this agent")
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

	items, err := s.agentTools(ctx, id, managedAgent.SystemSettingsToolsEnabled)
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

func (s *Server) agentTools(ctx context.Context, agentID string, includeSettings bool) ([]types.AgentTool, error) {
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
		items = append(items, systemAgentTool(def.Name, def.Description, agentToolSourceCore, agentToolReasonCoreSandbox, s.toolFamily(def.Name, agentToolSourceCore), false, toolInputSchema(def.InputSchema)))
	}

	// RunnerParams intentionally has no ForegroundHuman flag here. A profile
	// request has no trusted session context, so Settings actions are rendered as
	// policy metadata below instead of invoking settingspolicy.Available.
	params := agent.RunnerParams{UserID: info.UserID, AgentID: agentID}
	for _, entry := range s.builtinTools {
		def, ok := entry.Definition()
		if !ok || agent.IsCoreToolName(def.Name) {
			continue
		}
		if policy, isSettingsAction := settingspolicy.Lookup(def.Name); isSettingsAction {
			// A manager sees the enabled policy catalog. Its absence is the Profile's
			// authoritative disabled state; viewers receive neither signal.
			if includeSettings {
				items = append(items, systemAgentTool(def.Name, def.Description, agentToolSourceBuiltin, agentToolReasonSettingsPolicy, policy.Family, policy.AdminRequired, toolInputSchema(def.InputSchema)))
			}
			continue
		}

		available, err := builtinAvailable(ctx, entry, params)
		if err != nil {
			return nil, fmt.Errorf("resolve availability for tool %q: %w", def.Name, err)
		}
		if !available {
			items = append(items, runtimeUnavailableAgentTool(
				def.Name,
				def.Description,
				s.toolFamily(def.Name, agentToolSourceBuiltin),
				entry.UnavailableReason,
				toolInputSchema(def.InputSchema),
			))
			continue
		}
		decision := agent.ResolveToolOverride(true, def.Name, overrides)
		items = append(items, overrideAgentTool(def.Name, def.Description, agentToolSourceBuiltin, s.toolFamily(def.Name, agentToolSourceBuiltin), decision, toolInputSchema(def.InputSchema)))
	}

	if s.pluginHost != nil {
		for _, spec := range s.pluginHost.EnabledToolSpecs(ctx) {
			if agent.IsCoreToolName(spec.Name) {
				continue
			}
			decision := agent.ResolveToolOverride(true, spec.Name, overrides)
			items = append(items, overrideAgentTool(spec.Name, spec.Description, agentToolSourcePlugin, s.toolFamily(spec.Name, agentToolSourcePlugin), decision, nil))
		}
	}

	// MCP tools come from the resolved registrations' persisted catalogs, one
	// row per remote tool, override-controlled like builtins. The server-level
	// lifecycle stays on the MCP registration; an unhealthy server still lists
	// its tools with an availability_reason because the override is editable —
	// it just has no effect until the server is healthy again.
	if s.mcpSvc != nil {
		regs, err := s.mcpSvc.ResolveForContextWithShadowed(ctx, info.UserID, agentID)
		if err != nil {
			return nil, err
		}
		for _, resolved := range regs {
			reg := resolved.Registration
			reason := mcpAvailabilityReason(reg)
			for _, tool := range reg.Tools {
				name := mcp.NamespacedToolName(reg.Name, tool.Name)
				decision := agent.ResolveToolOverride(true, name, overrides)
				item := overrideAgentTool(name, tool.Description, agentToolSourceMCP, "mcp:"+reg.Name, decision, toolInputSchema(tool.InputSchema))
				if reason != "" {
					availability := types.AgentToolAvailabilityReason(reason)
					item.AvailabilityReason = &availability
				}
				items = append(items, item)
			}
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		left, right := agentToolSortFamily(items[i]), agentToolSortFamily(items[j])
		if left != right {
			return left < right
		}
		return items[i].Name < items[j].Name
	})
	return items, nil
}

func builtinAvailable(ctx context.Context, entry agent.BuiltinTool, params agent.RunnerParams) (bool, error) {
	if entry.Available == nil {
		return true, nil
	}
	return entry.Available(ctx, params)
}

func overrideAgentTool(name, description, source, family string, decision agent.ToolOverrideDecision, inputSchema *map[string]any) types.AgentTool {
	control := types.AgentToolControl(agentToolControlOverride)
	enabled := decision.Enabled
	origin := decision.Origin
	item := types.AgentTool{Name: name, Description: description, Source: source, Control: control, Enabled: &enabled, Origin: &origin, InputSchema: inputSchema}
	if family != "" {
		item.Family = &family
	}
	return item
}

func systemAgentTool(name, description, source, reason, family string, adminRequired bool, inputSchema *map[string]any) types.AgentTool {
	control := types.AgentToolControl(agentToolControlSystem)
	policyReason := types.AgentToolPolicyReason(reason)
	item := types.AgentTool{Name: name, Description: description, Source: source, Control: control, PolicyReason: &policyReason, InputSchema: inputSchema}
	if family != "" {
		item.Family = &family
	}
	if adminRequired {
		item.AdminRequired = &adminRequired
	}
	return item
}

// runtimeUnavailableAgentTool publishes a concrete prerequisite only after the
// runner's availability predicate has established that the builtin is absent.
// This keeps Profile setup CTAs tied to server-owned configuration state rather
// than a client-side name convention.
func runtimeUnavailableAgentTool(name, description, family string, availabilityReason agent.ToolUnavailableReason, inputSchema *map[string]any) types.AgentTool {
	item := systemAgentTool(name, description, agentToolSourceBuiltin, agentToolReasonRuntimeUnavailable, family, false, inputSchema)
	if availabilityReason == agent.ToolUnavailableReasonEmailConfigRequired {
		reason := types.EmailConfigRequired
		item.AvailabilityReason = &reason
	}
	return item
}

// mcpAvailabilityReason maps a registration's server-level state to the
// profile's availability enum. "unknown" (never probed) is reported as an
// error: the runner has no catalog to serve tools from until a probe runs.
func mcpAvailabilityReason(reg mcp.Registration) string {
	switch {
	case !reg.Enabled:
		return "mcp_server_disabled"
	case reg.Status == mcp.StatusNeedsAuth:
		return "mcp_needs_auth"
	case reg.Status != mcp.StatusOK:
		return "mcp_server_error"
	default:
		return ""
	}
}

// agentToolOverrideAllowed is the mutation-side counterpart of agentTools. It
// makes the API reject an override when the runner's own availability gate would
// ignore it, rather than returning a successful but ineffective mutation.
func (s *Server) agentToolOverrideAllowed(ctx context.Context, userID, agentID, name string) (bool, error) {
	if strings.HasPrefix(name, "mcp"+mcp.ToolNamespaceSep) {
		if s.mcpSvc == nil {
			return false, nil
		}
		regs, err := s.mcpSvc.ResolveForContextWithShadowed(ctx, userID, agentID)
		if err != nil {
			return false, err
		}
		for _, reg := range regs {
			for _, tool := range reg.Tools {
				if mcp.NamespacedToolName(reg.Name, tool.Name) == name {
					return true, nil
				}
			}
		}
		return false, nil
	}
	params := agent.RunnerParams{UserID: userID, AgentID: agentID}
	for _, entry := range s.builtinTools {
		definition, ok := entry.Definition()
		if !ok || definition.Name != name {
			continue
		}
		return builtinAvailable(ctx, entry, params)
	}
	if s.pluginHost != nil {
		for _, spec := range s.pluginHost.EnabledToolSpecs(ctx) {
			if spec.Name == name {
				return true, nil
			}
		}
	}
	return false, nil
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

// toolFamily is deliberately metadata-first for builtins: toolmeta declares
// generated builtin families, while hand-written or unknown surfaces fall back
// to a stable generic family. Never derive a family by splitting the tool name,
// because a plugin is free to use a generated-looking name.
func (s *Server) toolFamily(name, source string) string {
	if source == agentToolSourceBuiltin && s.toolMeta != nil {
		if family := s.toolMeta.Family(name); family != "" {
			return family
		}
	}
	switch source {
	case agentToolSourceCore:
		return agentToolFamilyCore
	case agentToolSourcePlugin:
		return agentToolFamilyPlugin
	default:
		return agentToolFamilyOther
	}
}

func agentToolSortFamily(item types.AgentTool) string {
	if item.Family != nil {
		return *item.Family
	}
	// MCP registrations intentionally live in their own top-level section.
	return "~mcp"
}

func isAgentToolOverrideScope(scope string) bool {
	return scope == agent.ToolOverrideScopeUserAgent || scope == agent.ToolOverrideScopeSystemAgent
}
