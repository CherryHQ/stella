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

func TestStellaSettingsCatalogIsPolicyManagedWithoutRunnerContext(t *testing.T) {
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

	stellaID := findStellaID(t, env)
	list := listAgentTools(t, env, env.bearerToken, stellaID)
	if availabilityCalls != 0 {
		t.Fatalf("Settings availability called %d times, want 0: profile has no trusted foreground session", availabilityCalls)
	}

	byName := make(map[string]types.AgentTool, len(list.Tools))
	for _, tool := range list.Tools {
		byName[tool.Name] = tool
	}
	for _, entry := range settingspolicy.Catalog() {
		tool, ok := byName[entry.Name]
		if !ok {
			t.Errorf("Settings action %q missing from Stella catalog", entry.Name)
			continue
		}
		if tool.Control != "system" || tool.Enabled != nil || tool.Origin != nil || tool.Family == nil || string(*tool.Family) != entry.Family || tool.PolicyReason == nil || string(*tool.PolicyReason) != "settings_policy" {
			t.Errorf("Settings action %q metadata = %#v, want policy-managed catalog row", entry.Name, tool)
		}
		if got := tool.AdminRequired != nil && *tool.AdminRequired; got != entry.AdminRequired {
			t.Errorf("Settings action %q admin_required = %t, want %t", entry.Name, got, entry.AdminRequired)
		}
	}

	ordinaryID := createAgentAsUser(t, env, env.bearerToken, "ordinary tool catalog")
	ordinary := listAgentTools(t, env, env.bearerToken, ordinaryID)
	for _, tool := range ordinary.Tools {
		if _, isSettingsAction := settingspolicy.Lookup(tool.Name); isSettingsAction {
			t.Fatalf("ordinary agent exposes Settings action %#v", tool)
		}
	}

	rr := doRequestWithSession(t, env.srv, env.bearerToken, http.MethodPatch, "/api/agents/"+stellaID+"/tools/agent_list", map[string]any{"enabled": false})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("policy-managed override status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
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
