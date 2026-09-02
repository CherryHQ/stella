// Package settingspolicy owns the narrow discovery and turn-capability boundary
// for conversational Settings tools. It deliberately knows no domain service or
// operation schema.
package settingspolicy

import (
	"context"
	"errors"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/platform/config"
	pkgtools "github.com/CherryHQ/stella/pkg/tools"
)

// AdminLookup resolves whether a user is currently active and an administrator.
// Errors intentionally fail closed because a partial settings catalog would make
// a missing capability look real.
type AdminLookup interface {
	IsAdmin(context.Context, string) (bool, error)
}

// AgentLookup reads the durable Agent capability bit. It stays narrow so the
// policy cannot reach unrelated configuration or reconstruct Agent ownership.
type AgentLookup interface {
	GetAgent(context.Context, string) (config.Agent, error)
}

var errDisabled = errors.New("system settings tools are disabled for this agent")

const (
	FamilyAgentManagement       = "agent_management"
	FamilyKnowledgeAndSkills    = "knowledge_and_skills"
	FamilyModelsAndDeployment   = "models_and_deployment"
	FamilyExtensionsAndConnects = "extensions_and_connections"
)

// CatalogEntry is policy metadata for a cold Settings action. It is deliberately
// separate from runtime availability: a profile request cannot prove that it is
// a foreground human turn, so it may describe this catalog but never claim the
// action is registered in a runner.
type CatalogEntry struct {
	Name          string
	Family        string
	AdminRequired bool
}

var catalog = []CatalogEntry{
	{Name: "settings_agent_list", Family: FamilyAgentManagement},
	{Name: "settings_agent_get", Family: FamilyAgentManagement},
	{Name: "settings_agent_create", Family: FamilyAgentManagement},
	{Name: "settings_agent_update", Family: FamilyAgentManagement},
	{Name: "settings_agent_delete", Family: FamilyAgentManagement},
	{Name: "settings_agent_tool_list", Family: FamilyAgentManagement},
	{Name: "settings_agent_tool_update", Family: FamilyAgentManagement},
	{Name: "settings_agent_tool_delete", Family: FamilyAgentManagement},
	{Name: "settings_library_file_list", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_library_file_get", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_library_file_upload", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_library_file_delete", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_skill_list", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_skill_get", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_skill_create", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_skill_update", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_skill_delete", Family: FamilyKnowledgeAndSkills},
	{Name: "settings_provider_list", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_provider_get", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_provider_create", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_provider_update", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_provider_delete", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_default_model_get", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_default_model_update", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_embedding_setting_get", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_embedding_setting_update", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "settings_plugin_list", Family: FamilyExtensionsAndConnects, AdminRequired: true},
	{Name: "settings_plugin_enable", Family: FamilyExtensionsAndConnects, AdminRequired: true},
	{Name: "settings_plugin_disable", Family: FamilyExtensionsAndConnects, AdminRequired: true},
	{Name: "settings_mcp_server_list", Family: FamilyExtensionsAndConnects},
	{Name: "settings_mcp_server_get", Family: FamilyExtensionsAndConnects},
	{Name: "settings_mcp_server_create", Family: FamilyExtensionsAndConnects},
	{Name: "settings_mcp_server_update", Family: FamilyExtensionsAndConnects},
	{Name: "settings_mcp_server_delete", Family: FamilyExtensionsAndConnects},
}

// Catalog returns a copy so callers cannot alter the policy inventory.
func Catalog() []CatalogEntry { return append([]CatalogEntry(nil), catalog...) }

// Lookup returns the catalog policy for one generated Settings action.
func Lookup(name string) (CatalogEntry, bool) {
	for _, entry := range catalog {
		if entry.Name == name {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

// ToolNames returns the policy inventory for worker exclusions.
func ToolNames() []string {
	names := make([]string, 0, len(catalog))
	for _, entry := range catalog {
		names = append(names, entry.Name)
	}
	return names
}

// AdminToolNames returns the Settings actions whose runner registration also
// needs a durable admin lookup.
func AdminToolNames() []string {
	names := make([]string, 0, len(catalog))
	for _, entry := range catalog {
		if entry.AdminRequired {
			names = append(names, entry.Name)
		}
	}
	return names
}

// Available gates a Settings tool's catalog registration. Discovery requires a
// durable opt-in on this exact Agent plus a direct foreground human turn. Domain
// authorization remains mandatory on Execute because a cached runner can outlive
// a policy or role change.
func Available(adminOnly bool, admins AdminLookup, agents AgentLookup) func(context.Context, runtime.RunnerParams) (bool, error) {
	return func(ctx context.Context, params runtime.RunnerParams) (bool, error) {
		if params.UserID == "" || params.AgentID == "" || params.GroupID != "" || params.GuestID != "" || !params.ForegroundHuman {
			return false, nil
		}
		enabled, err := enabled(ctx, agents, params.AgentID)
		if err != nil {
			return false, fmt.Errorf("resolve settings agent policy: %w", err)
		}
		if !enabled {
			return false, nil
		}
		if !adminOnly {
			return true, nil
		}
		if admins == nil {
			return false, fmt.Errorf("resolve settings admin: lookup unavailable")
		}
		admin, err := admins.IsAdmin(ctx, params.UserID)
		if err != nil {
			return false, fmt.Errorf("resolve settings admin: %w", err)
		}
		return admin, nil
	}
}

// enabled is the durable policy read shared by runner discovery and Execute.
func enabled(ctx context.Context, agents AgentLookup, agentID string) (bool, error) {
	if agents == nil {
		return false, errors.New("agent lookup unavailable")
	}
	if agentID == "" {
		return false, nil
	}
	agent, err := agents.GetAgent(ctx, agentID)
	if err != nil {
		return false, err
	}
	return agent.SystemSettingsToolsEnabled, nil
}

// DirectAuthority returns the direct human capability installed for this turn.
// Registry visibility is only discovery; adapters must call this again before a
// Settings operation reaches its domain service.
func DirectAuthority(ctx context.Context, runtimeUserID string) (authz.Authority, error) {
	authority, ok := authz.AuthorityFromContext(ctx)
	if !ok || authority.Kind() != authz.ActorUser || string(authority.UserID()) != runtimeUserID {
		return authz.Authority{}, authz.ErrUnauthenticated
	}
	return authority, nil
}

// allowExecute rechecks durable Settings policy before every call. A runner cache
// is a discovery optimization, never an authority cache: revocation, role changes,
// and deactivation must reject a tool left in an already-constructed runner. The
// wrapped domain tool still performs its own Authority/PEP decision for the
// requested action.
func allowExecute(ctx context.Context, agents AgentLookup, admins AdminLookup, adminRequired bool) error {
	if authz.GroupIDFromContext(ctx) != "" || authz.GuestIDFromContext(ctx) != "" {
		return errDisabled
	}
	userID, agentID := authz.UserIDFromContext(ctx), authz.AgentIDFromContext(ctx)
	if _, err := DirectAuthority(ctx, userID); err != nil {
		return errDisabled
	}
	enabled, err := enabled(ctx, agents, agentID)
	if err != nil || !enabled {
		return errDisabled
	}
	if adminRequired {
		if admins == nil {
			return errDisabled
		}
		admin, err := admins.IsAdmin(ctx, userID)
		if err != nil || !admin {
			return errDisabled
		}
	}
	return nil
}

type guardedTool struct {
	inner  pkgtools.Tool
	agents AgentLookup
	admins AdminLookup
}

func (t guardedTool) Definition() pkgtools.Definition { return t.inner.Definition() }

func (t guardedTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	entry, ok := Lookup(t.inner.Definition().Name)
	if !ok {
		return "", errDisabled
	}
	if err := allowExecute(ctx, t.agents, t.admins, entry.AdminRequired); err != nil {
		return "", err
	}
	return t.inner.Execute(ctx, args)
}

// Wrap makes durable policy enforcement unavoidable for a registered Settings
// tool. It is intentionally applied at composition rather than copied into all
// 34 generated adapters.
func Wrap(tool pkgtools.Tool, agents AgentLookup, admins AdminLookup) pkgtools.Tool {
	if tool == nil {
		return nil
	}
	return guardedTool{inner: tool, agents: agents, admins: admins}
}
