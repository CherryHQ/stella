package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/authz"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/platform/config"
	"github.com/CherryHQ/stella/pkg/toolmeta"
	"github.com/CherryHQ/stella/pkg/tools"
)

const agentToolListSibling = "settings_agent_list"

var agentManagementDescriptions = map[string]string{
	"list":   "List up to the requested number of agents you can use or manage. Results say when more agents exist.",
	"get":    "Read one agent's safe configuration and version. Use its version for settings_agent_update or settings_agent_delete.",
	"create": "Create an agent without provider credentials. The result includes the server-selected ID; add credentials only in the Web UI.",
	"update": "Update an agent using the version from settings_agent_get. Re-read the agent if the version has changed.",
	"delete": "Delete an agent using the version from settings_agent_get. This is irreversible and refuses a stale version.",
}

var agentOverrideDescriptions = map[string]string{
	"list":   "List generated tools and their exact user-agent override versions for one manageable agent.",
	"update": "Set one tool override using the version from settings_agent_tool_list. A first override requires the absent version.",
	"delete": "Clear one tool override using the version from settings_agent_tool_list, restoring the default visibility decision.",
}

// ManagementTool is one exact Agent or Agent-tool-override action. It is kept
// separate from the runtime's ordinary AgentActor tools: Settings require the
// fresh direct-human Authority installed only for an admitted Stella turn.
type ManagementTool struct {
	agentSpec    *SettingsAgentActionTool
	overrideSpec *SettingsAgentToolActionTool
	management   func() *agentaccess.Management
	overrides    *ToolOverrideStore
	registry     func() *toolmeta.Registry
	mcpCatalog   MCPCatalogFunc
}

// MCPCatalogFunc returns the MCP tool names currently in the resolved
// registrations' persisted catalogs for one (user, agent), mapped to their
// family ("mcp:<server>"). It is a func, not an import: internal/mcp imports
// this package for pool invalidation, so the dependency arrow cannot reverse.
type MCPCatalogFunc func(ctx context.Context, userID, agentID string) map[string]string

func NewManagementTool(spec SettingsAgentActionTool, management func() *agentaccess.Management) *ManagementTool {
	return &ManagementTool{agentSpec: &spec, management: management}
}

func NewToolOverrideManagementTool(spec SettingsAgentToolActionTool, management func() *agentaccess.Management, overrides *ToolOverrideStore, registry func() *toolmeta.Registry, mcpCatalog MCPCatalogFunc) *ManagementTool {
	return &ManagementTool{overrideSpec: &spec, management: management, overrides: overrides, registry: registry, mcpCatalog: mcpCatalog}
}

func (t *ManagementTool) Definition() tools.Definition {
	if t.agentSpec != nil {
		return t.agentSpec.Definition(agentManagementDescriptions[t.agentSpec.Action])
	}
	return t.overrideSpec.Definition(agentOverrideDescriptions[t.overrideSpec.Action])
}

func (t *ManagementTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	if t == nil || t.management == nil {
		return "", fmt.Errorf("agent management is unavailable — try again later")
	}
	management := t.management()
	if management == nil {
		return "", fmt.Errorf("agent management is unavailable — try again later")
	}
	userID := authz.UserIDFromContext(ctx)
	authority, err := authz.DirectAuthority(ctx, userID)
	if err != nil {
		return "", authz.MapToolError(t.toolName(), agentToolListSibling, err)
	}
	var out any
	if t.agentSpec != nil {
		out, err = SettingsAgentDispatch(ctx, agentManagementHandler{management: management, authority: authority}, t.agentSpec.Action, args)
	} else {
		if t.overrides == nil || t.registry == nil || t.registry() == nil {
			return "", fmt.Errorf("agent tool override management is unavailable — try again later")
		}
		out, err = SettingsAgentToolDispatch(ctx, agentOverrideHandler{management: management, authority: authority, overrides: t.overrides, registry: t.registry(), mcpCatalog: t.mcpCatalog}, t.overrideSpec.Action, args)
	}
	if err != nil {
		return "", authz.MapToolError(t.toolName(), agentToolListSibling, err)
	}
	return tools.MarshalResult(out)
}

func (t *ManagementTool) toolName() string {
	if t.agentSpec != nil {
		return t.agentSpec.Name
	}
	return t.overrideSpec.Name
}

type agentManagementHandler struct {
	management *agentaccess.Management
	authority  authz.Authority
}

func (h agentManagementHandler) List(ctx context.Context, in SettingsAgentListInput) (any, error) {
	limit := 50
	if in.Limit != 0 {
		limit = in.Limit
	}
	agents, truncated, err := h.management.ListForTool(ctx, h.authority, limit)
	if err != nil {
		return nil, err
	}
	out := make([]agentToolView, 0, len(agents))
	for _, agent := range agents {
		out = append(out, projectAgent(agent))
	}
	return map[string]any{"agents": out, "truncated": truncated}, nil
}

func (h agentManagementHandler) Get(ctx context.Context, in SettingsAgentGetInput) (any, error) {
	agent, err := h.management.GetForTool(ctx, h.authority, in.Id)
	if err != nil {
		return nil, err
	}
	return projectAgent(agent), nil
}

func (h agentManagementHandler) Create(ctx context.Context, in SettingsAgentCreateInput) (any, error) {
	candidate := config.Agent{
		ID: slugAgentID(in.Id, in.Name), Name: in.Name, Model: in.Model,
		ModelThinking: in.ModelThinking, ModelStrong: in.ModelStrong,
		ModelStrongThinking: in.ModelStrongThinking, ModelFast: in.ModelFast,
		ModelFastThinking: in.ModelFastThinking, SystemPrompt: in.SystemPrompt,
		Soul: in.Soul, Scope: in.Scope, Enabled: true,
	}
	if in.Enabled != nil {
		candidate.Enabled = *in.Enabled
	}
	if err := validateToolAgent(candidate); err != nil {
		return nil, err
	}
	created, err := h.management.CreateForTool(ctx, h.authority, candidate)
	if err != nil {
		return nil, err
	}
	return projectAgent(created), nil
}

func (h agentManagementHandler) Update(ctx context.Context, in SettingsAgentUpdateInput) (any, error) {
	current, err := h.management.GetForTool(ctx, h.authority, in.Id)
	if err != nil {
		return nil, err
	}
	candidate := current.Agent
	if in.Name != "" {
		candidate.Name = in.Name
	}
	if in.Model != nil {
		candidate.Model = *in.Model
	}
	if in.ModelThinking != "" {
		candidate.ModelThinking = in.ModelThinking
	}
	if in.ModelStrong != "" {
		candidate.ModelStrong = in.ModelStrong
	}
	if in.ModelStrongThinking != "" {
		candidate.ModelStrongThinking = in.ModelStrongThinking
	}
	if in.ModelFast != "" {
		candidate.ModelFast = in.ModelFast
	}
	if in.ModelFastThinking != "" {
		candidate.ModelFastThinking = in.ModelFastThinking
	}
	if in.SystemPrompt != nil {
		candidate.SystemPrompt = *in.SystemPrompt
	}
	if in.Soul != nil {
		candidate.Soul = *in.Soul
	}
	if in.Scope != "" {
		candidate.Scope = in.Scope
	}
	if in.Enabled != nil {
		candidate.Enabled = *in.Enabled
	}
	if err := validateToolAgent(candidate); err != nil {
		return nil, err
	}
	if _, _, err := h.management.UpdateIfVersion(ctx, h.authority, candidate, in.ExpectedVersion); err != nil {
		return nil, err
	}
	// Re-read through the PEP snapshot boundary so a successful response never
	// combines this update's fields with a later version (or vice versa).
	updated, err := h.management.GetForTool(ctx, h.authority, candidate.ID)
	if err != nil {
		return nil, err
	}
	return projectAgent(updated), nil
}

func (h agentManagementHandler) Delete(ctx context.Context, in SettingsAgentDeleteInput) (any, error) {
	if err := h.management.DeleteIfVersion(ctx, h.authority, in.Id, in.ExpectedVersion); err != nil {
		return nil, err
	}
	return map[string]string{"id": in.Id, "status": "deleted"}, nil
}

type agentOverrideHandler struct {
	management *agentaccess.Management
	authority  authz.Authority
	overrides  *ToolOverrideStore
	registry   *toolmeta.Registry
	mcpCatalog MCPCatalogFunc
}

func (h agentOverrideHandler) List(ctx context.Context, in SettingsAgentToolListInput) (any, error) {
	if err := h.management.ManageForTool(ctx, h.authority, in.TargetAgentId); err != nil {
		return nil, err
	}
	versions, err := h.overrides.ListVersions(ctx, string(h.authority.UserID()), in.TargetAgentId)
	if err != nil {
		return nil, err
	}
	items := make([]ToolOverrideVersion, 0, len(h.registry.Names()))
	for _, name := range h.registry.Names() {
		if !h.managedTool(ctx, in.TargetAgentId, name) {
			continue
		}
		item, ok := versions[name]
		if !ok {
			item = ToolOverrideVersion{ToolName: name, Scope: ToolOverrideScopeUserAgent, Version: ToolOverrideAbsentVersion}
		}
		items = append(items, item)
	}
	// MCP tools are not in the toolmeta registry: they come from the resolved
	// registrations' persisted catalogs, keyed by family for UI grouping.
	for name, family := range h.catalog(ctx, in.TargetAgentId) {
		item, ok := versions[name]
		if !ok {
			item = ToolOverrideVersion{ToolName: name, Scope: ToolOverrideScopeUserAgent, Version: ToolOverrideAbsentVersion}
		}
		item.Family = family
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ToolName < items[j].ToolName })
	return map[string]any{"tools": items}, nil
}

func (h agentOverrideHandler) Update(ctx context.Context, in SettingsAgentToolUpdateInput) (any, error) {
	if err := h.management.ManageForTool(ctx, h.authority, in.TargetAgentId); err != nil {
		return nil, err
	}
	if !h.managedTool(ctx, in.TargetAgentId, in.ToolName) {
		return nil, fmt.Errorf("tool is not managed here")
	}
	key := h.overrideKey(in.TargetAgentId, ToolOverrideScopeUserAgent, in.ToolName)
	item, err := h.overrides.SetIfVersion(ctx, ToolOverrideWrite{ToolName: key.ToolName, Scope: key.Scope, UserID: key.UserID, AgentID: key.AgentID, Enabled: in.Enabled}, in.ExpectedVersion)
	if err != nil {
		return nil, mapOverrideConflict(err)
	}
	h.management.ReloadForTool(ctx, in.TargetAgentId)
	return item, nil
}

func (h agentOverrideHandler) Delete(ctx context.Context, in SettingsAgentToolDeleteInput) (any, error) {
	if err := h.management.ManageForTool(ctx, h.authority, in.TargetAgentId); err != nil {
		return nil, err
	}
	if !h.managedTool(ctx, in.TargetAgentId, in.ToolName) {
		return nil, fmt.Errorf("tool is not managed here")
	}
	key := h.overrideKey(in.TargetAgentId, ToolOverrideScopeUserAgent, in.ToolName)
	if err := h.overrides.ClearIfVersion(ctx, key, in.ExpectedVersion); err != nil {
		return nil, mapOverrideConflict(err)
	}
	h.management.ReloadForTool(ctx, in.TargetAgentId)
	return map[string]string{"tool_name": in.ToolName, "status": "default"}, nil
}

// managedTool accepts a name only when a runner in this context can actually
// register it: generated registry entries, or MCP tools present in the
// resolved registrations' persisted catalogs. The mcp__ name prefix alone is
// not enough — a stale override for a since-removed server tool must stay
// invisible rather than look managed.
func (h agentOverrideHandler) managedTool(ctx context.Context, targetAgentID, name string) bool {
	if IsCoreToolName(name) {
		return false
	}
	if _, settingsManaged := settingspolicy.Lookup(name); settingsManaged {
		return false
	}
	if _, ok := h.registry.Lookup(name); ok {
		return true
	}
	_, ok := h.catalog(ctx, targetAgentID)[name]
	return ok
}

func (h agentOverrideHandler) catalog(ctx context.Context, targetAgentID string) map[string]string {
	if h.mcpCatalog == nil {
		return nil
	}
	return h.mcpCatalog(ctx, string(h.authority.UserID()), targetAgentID)
}

func (h agentOverrideHandler) overrideKey(targetID, scope, toolName string) ToolOverrideKey {
	key := ToolOverrideKey{ToolName: toolName, Scope: scope}
	if scope == ToolOverrideScopeUser || scope == ToolOverrideScopeUserAgent {
		key.UserID = string(h.authority.UserID())
	}
	if scope == ToolOverrideScopeSystemAgent || scope == ToolOverrideScopeUserAgent {
		key.AgentID = targetID
	}
	return key
}

func mapOverrideConflict(err error) error {
	if errors.Is(err, config.ErrAgentVersionConflict) {
		return agentaccess.ErrConflict
	}
	return err
}

type agentToolView struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Model               string `json:"model,omitempty"`
	ModelThinking       string `json:"model_thinking,omitempty"`
	ModelStrong         string `json:"model_strong,omitempty"`
	ModelStrongThinking string `json:"model_strong_thinking,omitempty"`
	ModelFast           string `json:"model_fast,omitempty"`
	ModelFastThinking   string `json:"model_fast_thinking,omitempty"`
	SystemPrompt        string `json:"system_prompt,omitempty"`
	Soul                string `json:"soul,omitempty"`
	Scope               string `json:"scope"`
	Enabled             bool   `json:"enabled"`
	Version             string `json:"version"`
}

func projectAgent(in agentaccess.ToolAgent) agentToolView {
	a := in.Agent
	return agentToolView{ID: a.ID, Name: a.Name, Model: a.Model, ModelThinking: a.ModelThinking, ModelStrong: a.ModelStrong, ModelStrongThinking: a.ModelStrongThinking, ModelFast: a.ModelFast, ModelFastThinking: a.ModelFastThinking, SystemPrompt: a.SystemPrompt, Soul: a.Soul, Scope: a.Scope, Enabled: a.Enabled, Version: in.Version}
}

func validateToolAgent(a config.Agent) error {
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if !config.ValidThinkingLevel(a.ModelThinking) || !config.ValidThinkingLevel(a.ModelStrongThinking) || !config.ValidThinkingLevel(a.ModelFastThinking) {
		return fmt.Errorf("invalid thinking level")
	}
	for _, value := range []string{a.Model, a.ModelStrong, a.ModelFast} {
		if !config.ValidModelRef(value) {
			return fmt.Errorf("invalid model reference")
		}
	}
	return nil
}

func slugAgentID(id, name string) string {
	if strings.TrimSpace(id) != "" {
		return id
	}
	var out []rune
	lastDash := true
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			out, lastDash = append(out, r), false
		} else if !lastDash {
			out, lastDash = append(out, '-'), true
		}
	}
	value := strings.Trim(string(out), "-")
	if value == "" {
		return "agent"
	}
	return value
}
