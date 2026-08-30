// Package settingspolicy owns the narrow discovery and turn-capability boundary
// for Stella's conversational Settings tools. It deliberately knows no domain
// service or operation schema.
package settingspolicy

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/agent/runtime"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/store"
)

// AdminLookup resolves durable user privilege. Errors intentionally fail closed
// because a partial settings catalog would make a missing capability look real.
type AdminLookup interface {
	IsAdmin(context.Context, string) (bool, error)
}

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
	{Name: "agent_list", Family: FamilyAgentManagement},
	{Name: "agent_get", Family: FamilyAgentManagement},
	{Name: "agent_create", Family: FamilyAgentManagement},
	{Name: "agent_update", Family: FamilyAgentManagement},
	{Name: "agent_delete", Family: FamilyAgentManagement},
	{Name: "agent_tool_list", Family: FamilyAgentManagement},
	{Name: "agent_tool_update", Family: FamilyAgentManagement},
	{Name: "agent_tool_delete", Family: FamilyAgentManagement},
	{Name: "library_file_list", Family: FamilyKnowledgeAndSkills},
	{Name: "library_file_get", Family: FamilyKnowledgeAndSkills},
	{Name: "library_file_upload", Family: FamilyKnowledgeAndSkills},
	{Name: "library_file_delete", Family: FamilyKnowledgeAndSkills},
	{Name: "skill_list", Family: FamilyKnowledgeAndSkills},
	{Name: "skill_get", Family: FamilyKnowledgeAndSkills},
	{Name: "skill_create", Family: FamilyKnowledgeAndSkills},
	{Name: "skill_update", Family: FamilyKnowledgeAndSkills},
	{Name: "skill_delete", Family: FamilyKnowledgeAndSkills},
	{Name: "provider_list", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "provider_get", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "provider_create", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "provider_update", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "provider_delete", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "default_model_get", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "default_model_update", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "embedding_setting_get", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "embedding_setting_update", Family: FamilyModelsAndDeployment, AdminRequired: true},
	{Name: "plugin_list", Family: FamilyExtensionsAndConnects, AdminRequired: true},
	{Name: "plugin_enable", Family: FamilyExtensionsAndConnects, AdminRequired: true},
	{Name: "plugin_disable", Family: FamilyExtensionsAndConnects, AdminRequired: true},
	{Name: "mcp_server_list", Family: FamilyExtensionsAndConnects},
	{Name: "mcp_server_get", Family: FamilyExtensionsAndConnects},
	{Name: "mcp_server_create", Family: FamilyExtensionsAndConnects},
	{Name: "mcp_server_update", Family: FamilyExtensionsAndConnects},
	{Name: "mcp_server_delete", Family: FamilyExtensionsAndConnects},
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

// Available gates a Settings tool's catalog registration. Execute-time domain
// authorization remains mandatory because a cached runner can outlive a role
// change.
func Available(adminOnly bool, lookup AdminLookup) func(context.Context, runtime.RunnerParams) (bool, error) {
	return func(ctx context.Context, params runtime.RunnerParams) (bool, error) {
		if params.UserID == "" || params.AgentID == "" || params.GroupID != "" || params.GuestID != "" ||
			params.AgentID != store.DefaultStellaAgentID || !params.ForegroundHuman {
			return false, nil
		}
		if !adminOnly {
			return true, nil
		}
		if lookup == nil {
			return false, fmt.Errorf("resolve settings admin: lookup unavailable")
		}
		admin, err := lookup.IsAdmin(ctx, params.UserID)
		if err != nil {
			return false, fmt.Errorf("resolve settings admin: %w", err)
		}
		return admin, nil
	}
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
