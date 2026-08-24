package server_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	"github.com/CherryHQ/stella/internal/server"
	"github.com/CherryHQ/stella/pkg/tools"
)

func TestUpdateAgentToolWritesEachScope(t *testing.T) {
	env := setupAdmin(t)
	env.rebuild(t, func(d *server.Deps) { d.BuiltinTools = []agent.BuiltinTool{{Tool: fakeManagedTool{name: "vault"}}} })
	user, userSession := newNonAdmin(t, env, "tool-scope-user")
	agentID := createAgentAsUser(t, env, userSession, "tool-scope-agent")

	cases := []struct {
		name        string
		scope       string
		session     string
		wantUserID  string
		wantAgentID string
	}{
		{name: "user", scope: "user", session: userSession, wantUserID: user.ID},
		{name: "user_agent", scope: "user_agent", session: userSession, wantUserID: user.ID, wantAgentID: agentID},
		{name: "system", scope: "system", session: env.bearerToken},
		{name: "system_agent", scope: "system_agent", session: env.bearerToken, wantAgentID: agentID},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setToolOverride(t, env, tc.session, agentID, tc.scope, http.StatusOK)
			assertToolOverride(t, env, "vault", tc.scope, tc.wantUserID, tc.wantAgentID, false)

			deleteToolOverride(t, env, tc.session, agentID, tc.scope, http.StatusOK)
			assertNoToolOverride(t, env, "vault", tc.scope, tc.wantUserID, tc.wantAgentID)
		})
	}
}

func TestAgentToolsExposeAndManageRuntimeSkills(t *testing.T) {
	env := setupAdmin(t)
	user, userSession := newNonAdmin(t, env, "runtime-skills-user")
	agentID := createAgentAsUser(t, env, userSession, "runtime-skills-agent")

	rr := doRequestWithSession(t, env.srv, userSession, http.MethodGet, "/api/agents/"+agentID+"/tools", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list tools status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	var list struct {
		Tools []struct {
			Name    string `json:"name"`
			Source  string `json:"source"`
			Enabled bool   `json:"enabled"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &list); err != nil {
		t.Fatalf("decode tools: %v", err)
	}
	found := false
	for _, tool := range list.Tools {
		if tool.Name == "skills" {
			found = true
			if tool.Source != "builtin" || !tool.Enabled {
				t.Fatalf("skills inventory = %+v, want enabled builtin", tool)
			}
		}
	}
	if !found {
		t.Fatal("runtime skills tool is absent from inventory")
	}

	rr = doRequestWithSession(t, env.srv, userSession, http.MethodPatch, "/api/agents/"+agentID+"/tools/skills", map[string]any{
		"enabled": false,
		"scope":   "user_agent",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable skills status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	assertToolOverride(t, env, "skills", "user_agent", user.ID, agentID, false)

	deleteToolOverrideNamed(t, env, userSession, agentID, "skills", "user_agent", http.StatusOK)
	assertNoToolOverride(t, env, "skills", "user_agent", user.ID, agentID)
}

func TestUpdateAgentToolRejectsSystemScopesForNonAdmin(t *testing.T) {
	env := setupAdmin(t)
	env.rebuild(t, func(d *server.Deps) { d.BuiltinTools = []agent.BuiltinTool{{Tool: fakeManagedTool{name: "vault"}}} })
	_, userSession := newNonAdmin(t, env, "tool-scope-denied")
	agentID := createAgentAsUser(t, env, userSession, "tool-scope-denied-agent")

	for _, scope := range []string{"system", "system_agent"} {
		t.Run(scope, func(t *testing.T) {
			setToolOverride(t, env, userSession, agentID, scope, http.StatusForbidden)
		})
	}
}

func setToolOverride(t *testing.T, env *testEnv, sessionID string, agentID string, scope string, wantStatus int) {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodPatch, "/api/agents/"+agentID+"/tools/vault", map[string]any{
		"enabled": false,
		"scope":   scope,
	})
	if rr.Code != wantStatus {
		t.Fatalf("set tool override status = %d, want %d (body: %s)", rr.Code, wantStatus, rr.Body.String())
	}
}

func deleteToolOverride(t *testing.T, env *testEnv, sessionID string, agentID string, scope string, wantStatus int) {
	t.Helper()
	deleteToolOverrideNamed(t, env, sessionID, agentID, "vault", scope, wantStatus)
}

func deleteToolOverrideNamed(t *testing.T, env *testEnv, sessionID string, agentID, toolName, scope string, wantStatus int) {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sessionID, http.MethodPatch, "/api/agents/"+agentID+"/tools/"+toolName, map[string]any{
		"scope": scope,
	})
	if rr.Code != wantStatus {
		t.Fatalf("delete tool override status = %d, want %d (body: %s)", rr.Code, wantStatus, rr.Body.String())
	}
}

func assertToolOverride(t *testing.T, env *testEnv, toolName, scope, wantUserID, wantAgentID string, wantEnabled bool) {
	t.Helper()
	var userID sql.NullString
	var agentID sql.NullString
	var enabled bool
	err := env.db.QueryRow(context.Background(), `
		SELECT user_id::text, agent_id, enabled
		FROM tool_override
		WHERE tool_name = $1
		  AND scope = $2
		  AND coalesce(user_id::text, '') = $3
		  AND coalesce(agent_id, '') = $4
	`, toolName, scope, wantUserID, wantAgentID).Scan(&userID, &agentID, &enabled)
	if err != nil {
		t.Fatalf("query tool_override %s: %v", scope, err)
	}
	if enabled != wantEnabled {
		t.Fatalf("enabled = %v, want %v", enabled, wantEnabled)
	}
	if got := nullStringValue(userID); got != wantUserID {
		t.Fatalf("user_id = %q, want %q", got, wantUserID)
	}
	if got := nullStringValue(agentID); got != wantAgentID {
		t.Fatalf("agent_id = %q, want %q", got, wantAgentID)
	}
}

func assertNoToolOverride(t *testing.T, env *testEnv, toolName, scope, userID, agentID string) {
	t.Helper()
	var count int
	if err := env.db.QueryRow(context.Background(), `
		SELECT count(*)
		FROM tool_override
		WHERE tool_name = $1
		  AND scope = $2
		  AND coalesce(user_id::text, '') = $3
		  AND coalesce(agent_id, '') = $4
	`, toolName, scope, userID, agentID).Scan(&count); err != nil {
		t.Fatalf("count tool_override %s: %v", scope, err)
	}
	if count != 0 {
		t.Fatalf("tool_override rows = %d, want 0", count)
	}
}

func nullStringValue(s sql.NullString) string {
	if !s.Valid {
		return ""
	}
	return s.String
}

type fakeManagedTool struct {
	name string
}

func (t fakeManagedTool) Definition() tools.Definition {
	return tools.Definition{Name: t.name, Description: "test tool"}
}

func (t fakeManagedTool) Execute(context.Context, map[string]any) (string, error) {
	return "", nil
}
