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

var toolNames = []string{
	"agent_list", "agent_get", "agent_create", "agent_update", "agent_delete",
	"agent_tool_list", "agent_tool_update", "agent_tool_delete",
	"library_file_list", "library_file_get", "library_file_upload", "library_file_delete",
	"skill_list", "skill_get", "skill_create", "skill_update", "skill_delete",
	"provider_list", "provider_get", "provider_create", "provider_update", "provider_delete",
	"default_model_get", "default_model_update", "embedding_setting_get", "embedding_setting_update",
	"plugin_list", "plugin_enable", "plugin_disable",
	"mcp_server_list", "mcp_server_get", "mcp_server_create", "mcp_server_update", "mcp_server_delete",
}

var adminToolNames = []string{
	"provider_list", "provider_get", "provider_create", "provider_update", "provider_delete",
	"default_model_get", "default_model_update", "embedding_setting_get", "embedding_setting_update",
	"plugin_list", "plugin_enable", "plugin_disable",
}

// ToolNames returns a copy so worker exclusions cannot mutate the policy set.
func ToolNames() []string { return append([]string(nil), toolNames...) }

// AdminToolNames returns the settings tools whose discovery additionally needs a
// durable admin lookup.
func AdminToolNames() []string { return append([]string(nil), adminToolNames...) }

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
