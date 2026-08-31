package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/agent/settingspolicy"
	"github.com/CherryHQ/stella/internal/server"
)

func TestSettingsCatalogFollowsOwnerManagedAgentPolicy(t *testing.T) {
	env := setupAdmin(t)
	availabilityCalls := 0
	env.rebuild(t, func(deps *server.Deps) {
		tools := make([]agent.BuiltinTool, 0, len(settingspolicy.Catalog()))
		for _, entry := range settingspolicy.Catalog() {
			tools = append(tools, agent.BuiltinTool{
				Tool: fakeManagedTool{name: entry.Name},
				Available: func(_ context.Context, _ agent.RunnerParams) (bool, error) {
					availabilityCalls++
					return false, nil
				},
			})
		}
		deps.BuiltinTools = tools
	})

	owner, ownerSession := newNonAdmin(t, env, "settings-catalog-owner")
	agentID := createAgentAsUser(t, env, ownerSession, "settings catalog agent")
	setSettingsToolsEnabled(t, env, agentID, true)

	list := listAgentTools(t, env, ownerSession, agentID)
	if availabilityCalls != 0 {
		t.Fatalf("Settings availability called %d times, want 0: profile has no trusted foreground session", availabilityCalls)
	}
	assertSettingsCatalog(t, list)

	setSettingsToolsEnabled(t, env, agentID, false)
	disabled := listAgentTools(t, env, ownerSession, agentID)
	assertNoSettingsCatalog(t, disabled)

	// A system-scoped Agent remains readable to an unrelated user, but Settings
	// policy state and catalog are an Agent-manage concern, not a viewer concern.
	managed, err := env.store.GetAgent(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	managed.Scope = "system"
	if err := env.store.UpdateAgent(t.Context(), managed); err != nil {
		t.Fatal(err)
	}
	_, viewerSession := newNonAdmin(t, env, "settings-catalog-viewer")
	viewer := listAgentTools(t, env, viewerSession, agentID)
	assertNoSettingsCatalog(t, viewer)

	rr := doRequestWithSession(t, env.srv, ownerSession, http.MethodPatch, "/api/agents/"+agentID+"/tools/settings_agent_list", map[string]any{"enabled": false})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("policy-managed override status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	_ = owner // owner existence documents that this is the creator/PEP path.
}

func setSettingsToolsEnabled(t *testing.T, env *testEnv, agentID string, enabled bool) {
	t.Helper()
	agent, err := env.store.GetAgent(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	agent.SystemSettingsToolsEnabled = enabled
	if err := env.store.UpdateAgent(t.Context(), agent); err != nil {
		t.Fatal(err)
	}
}

func assertSettingsCatalog(t *testing.T, list types.AgentToolList) {
	t.Helper()
	byName := make(map[string]types.AgentTool, len(list.Tools))
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
	}
	for _, entry := range settingspolicy.Catalog() {
		tool, ok := byName[entry.Name]
		if !ok {
			t.Errorf("Settings action %q missing from enabled manager catalog", entry.Name)
			continue
		}
		if tool.Control != "system" || tool.Enabled != nil || tool.Origin != nil || tool.Family == nil || *tool.Family != entry.Family || tool.PolicyReason == nil || string(*tool.PolicyReason) != "settings_policy" {
			t.Errorf("Settings action %q metadata = %#v, want policy-managed catalog row", entry.Name, tool)
		}
		if got := tool.AdminRequired != nil && *tool.AdminRequired; got != entry.AdminRequired {
			t.Errorf("Settings action %q admin_required = %t, want %t", entry.Name, got, entry.AdminRequired)
		}
	}
}

func assertNoSettingsCatalog(t *testing.T, list types.AgentToolList) {
	t.Helper()
	for _, tool := range list.Tools {
		if _, isSettingsAction := settingspolicy.Lookup(tool.Name); isSettingsAction {
			t.Fatalf("Settings action leaked from disabled or non-manager catalog: %#v", tool)
		}
	}
}

func listAgentTools(t *testing.T, env *testEnv, sessionID, agentID string) types.AgentToolList {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tools status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	response := parseResponse(t, rr)
	var list types.AgentToolList
	if err := json.Unmarshal(response.Data, &list); err != nil {
		t.Fatalf("unmarshal tool list: %v", err)
	}
	return list
}
