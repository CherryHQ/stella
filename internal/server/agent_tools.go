package server

import (
	"context"
	"fmt"
	"net/http"
	"sort"

	apiserver "github.com/CherryHQ/stella/api/server"
	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	coretools "github.com/CherryHQ/stella/internal/agent/sandbox"
	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/store"
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
	agentToolReasonMCPRegistration    = "mcp_registration"
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
		items = append(items, systemAgentTool(def.Name, def.Description, agentToolSourceCore, agentToolReasonCoreSandbox, "", false, toolInputSchema(def.InputSchema)))
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
			if agentID == store.DefaultStellaAgentID {
				items = append(items, systemAgentTool(def.Name, def.Description, agentToolSourceBuiltin, agentToolReasonSettingsPolicy, policy.Family, policy.AdminRequired, toolInputSchema(def.InputSchema)))
			}
			continue
		}

		available, err := builtinAvailable(ctx, entry, params)
		if err != nil {
			return nil, fmt.Errorf("resolve availability for tool %q: %w", def.Name, err)
		}
		if !available {
			items = append(items, systemAgentTool(def.Name, def.Description, agentToolSourceBuiltin, agentToolReasonRuntimeUnavailable, "", false, toolInputSchema(def.InputSchema)))
			continue
		}
		decision := agent.ResolveToolOverride(true, def.Name, overrides)
		items = append(items, overrideAgentTool(def.Name, def.Description, agentToolSourceBuiltin, decision, toolInputSchema(def.InputSchema)))
	}

	if s.pluginHost != nil {
		for _, spec := range s.pluginHost.EnabledToolSpecs(ctx) {
			if agent.IsCoreToolName(spec.Name) {
				continue
			}
			decision := agent.ResolveToolOverride(true, spec.Name, overrides)
			items = append(items, overrideAgentTool(spec.Name, spec.Description, agentToolSourcePlugin, decision, nil))
		}
	}

	// MCP registrations are intentionally separate from overrides: their server
	// lifecycle is managed by the MCP API, not by tool_override rows.
	if s.mcpSvc != nil {
		regs, err := s.mcpSvc.ResolveForContext(ctx, info.UserID, agentID)
		if err != nil {
			return nil, err
		}
		for _, reg := range regs {
			if agent.IsCoreToolName(reg.Name) {
				continue
			}
			items = append(items, systemAgentTool(reg.Name, reg.URL, agentToolSourceMCP, agentToolReasonMCPRegistration, "", false, nil))
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

func builtinAvailable(ctx context.Context, entry agent.BuiltinTool, params agent.RunnerParams) (bool, error) {
	if entry.Available == nil {
		return true, nil
	}
	return entry.Available(ctx, params)
}

func overrideAgentTool(name, description, source string, decision agent.ToolOverrideDecision, inputSchema *map[string]any) types.AgentTool {
	control := types.AgentToolControl(agentToolControlOverride)
	enabled := decision.Enabled
	origin := decision.Origin
	return types.AgentTool{Name: name, Description: description, Source: source, Control: control, Enabled: &enabled, Origin: &origin, InputSchema: inputSchema}
}

func systemAgentTool(name, description, source, reason, family string, adminRequired bool, inputSchema *map[string]any) types.AgentTool {
	control := types.AgentToolControl(agentToolControlSystem)
	policyReason := types.AgentToolPolicyReason(reason)
	item := types.AgentTool{Name: name, Description: description, Source: source, Control: control, PolicyReason: &policyReason, InputSchema: inputSchema}
	if family != "" {
		catalogFamily := types.AgentToolFamily(family)
		item.Family = &catalogFamily
	}
	if adminRequired {
		item.AdminRequired = &adminRequired
	}
	return item
}

// agentToolOverrideAllowed is the mutation-side counterpart of agentTools. It
// makes the API reject an override when the runner's own availability gate would
// ignore it, rather than returning a successful but ineffective mutation.
func (s *Server) agentToolOverrideAllowed(ctx context.Context, userID, agentID, name string) (bool, error) {
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
