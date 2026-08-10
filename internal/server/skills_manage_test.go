package server_test

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
)

// createScopedSkill posts to /api/skills as the given session and returns the
// new skill ID. It fails the test if the status is not 201.
func createScopedSkill(t *testing.T, env *testEnv, sid string, body map[string]any) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sid, "POST", "/api/skills", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create scoped skill %v: status = %d (body: %s)", body["scope"], rr.Code, rr.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &out); err != nil {
		t.Fatalf("unmarshal create response: %v", err)
	}
	assertFullSkillMutationResponse(t, rr, out.ID, "manual")
	return out.ID
}

// TestScopedSkills_CreatePermissions verifies the /api/skills scope guard:
// non-admins may manage only user/user_agent scopes; admins own the system tiers.
func TestScopedSkills_CreatePermissions(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "scoped-create")
	agentID := createAgentAsUser(t, env, sid, "scoped-create-agent")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"non-admin system forbidden", map[string]any{"name": "s1", "scope": "system"}, http.StatusForbidden},
		{"non-admin system_agent forbidden", map[string]any{"name": "s2", "scope": "system_agent", "agent_id": agentID}, http.StatusForbidden},
		{"non-admin user ok", map[string]any{"name": "u1", "scope": "user"}, http.StatusCreated},
		{"non-admin user_agent ok", map[string]any{"name": "ua1", "scope": "user_agent", "agent_id": agentID}, http.StatusCreated},
		{"user_agent requires agent_id", map[string]any{"name": "ua2", "scope": "user_agent"}, http.StatusBadRequest},
		{"invalid scope", map[string]any{"name": "x1", "scope": "bogus"}, http.StatusBadRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, sid, "POST", "/api/skills", c.body)
			if rr.Code != c.want {
				t.Fatalf("status = %d, want %d (body: %s)", rr.Code, c.want, rr.Body.String())
			}
		})
	}

	// Admin owns the system tiers.
	adminAgent := createAgentAsUser(t, env, env.bearerToken, "scoped-admin-agent")
	for _, body := range []map[string]any{
		{"name": "sys-global", "scope": "system"},
		{"name": "sys-agent", "scope": "system_agent", "agent_id": adminAgent},
	} {
		rr := doRequest(t, env, "POST", "/api/skills", body)
		if rr.Code != http.StatusCreated {
			t.Fatalf("admin create %v: status = %d (body: %s)", body["scope"], rr.Code, rr.Body.String())
		}
	}
}

func TestScopedSkills_AdminManagesGlobalSystemSkill(t *testing.T) {
	env := setupAdmin(t)
	source, err := filepath.Abs("../../resources/skills/system/stella")
	if err != nil {
		t.Fatalf("abs source: %v", err)
	}

	rr := doRequest(t, env, "POST", "/api/skills/install", map[string]any{
		"source": source,
		"scope":  "system",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("install system skill status = %d, want 201 (body: %s)", rr.Code, rr.Body.String())
	}
	var installed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &installed); err != nil {
		t.Fatalf("unmarshal install response: %v", err)
	}
	assertFullSkillMutationResponse(t, rr, installed.ID, "manual")

	rr = doRequest(t, env, "PATCH", "/api/skills/"+installed.ID, map[string]any{
		"disable_model_invocation": true,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("disable system skill status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	rr = doRequest(t, env, "DELETE", "/api/skills/"+installed.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete system skill status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestScopedSkills_CrossUserIsolation verifies a user cannot read or delete
// another user's user-scoped skill — existence is masked as 404.
func TestScopedSkills_CrossUserIsolation(t *testing.T) {
	env := setupAdmin(t)
	_, sidA := newNonAdmin(t, env, "scoped-iso-a")
	_, sidB := newNonAdmin(t, env, "scoped-iso-b")

	id := createScopedSkill(t, env, sidA, map[string]any{"name": "a-skill", "scope": "user"})

	rr := doRequestWithSession(t, env.srv, sidB, "GET", "/api/skills/"+id, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("B get status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
	rr = doRequestWithSession(t, env.srv, sidB, "DELETE", "/api/skills/"+id, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("B delete status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}

	// Owner still has access.
	rr = doRequestWithSession(t, env.srv, sidA, "GET", "/api/skills/"+id, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("A get status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
}

// TestScopedSkills_DeleteRemovesMutableRows verifies Settings deletion is
// permanent for every managed scope.
func TestScopedSkills_DeleteRemovesMutableRows(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "scoped-delete-lifecycle")
	agentID := createAgentAsUser(t, env, sid, "scoped-delete-lifecycle-agent")

	userID := createScopedSkill(t, env, sid, map[string]any{
		"name": "settings-delete-user", "scope": "user",
		"files": map[string]string{"SKILL.md": "# user", "reference.md": "keep user"},
	})
	userAgentID := createScopedSkill(t, env, sid, map[string]any{
		"name": "settings-delete-agent", "scope": "user_agent", "agent_id": agentID,
		"files": map[string]string{"SKILL.md": "# agent", "reference.md": "keep agent"},
	})
	systemAgentID := createScopedSkill(t, env, env.bearerToken, map[string]any{
		"name": "settings-delete-system-agent", "scope": "system_agent", "agent_id": agentID,
		"files": map[string]string{"SKILL.md": "# system agent", "reference.md": "keep system agent"},
	})
	systemID := createScopedSkill(t, env, env.bearerToken, map[string]any{
		"name": "settings-delete-system", "scope": "system",
		"files": map[string]string{"SKILL.md": "# system"},
	})

	for _, tc := range []struct {
		name, sid, id, scope, agent string
	}{
		{name: "user", sid: sid, id: userID, scope: "user"},
		{name: "user_agent", sid: sid, id: userAgentID, scope: "user_agent", agent: agentID},
		{name: "system", sid: env.bearerToken, id: systemID, scope: "system"},
		{name: "system_agent", sid: env.bearerToken, id: systemAgentID, scope: "system_agent", agent: agentID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := doRequestWithSession(t, env.srv, tc.sid, "DELETE", "/api/skills/"+tc.id, nil)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("delete status = %d, want 204 (body: %s)", rr.Code, rr.Body.String())
			}
			path := "/api/skills?scope=" + tc.scope
			if tc.agent != "" {
				path += "&agent_id=" + tc.agent
			}
			rr = doRequestWithSession(t, env.srv, tc.sid, "GET", path, nil)
			if rr.Code != http.StatusOK || findSkill(decodeSkillList(t, rr), "settings-delete-"+tc.name) != nil {
				t.Fatalf("default list still contains removed skill: status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}

	for _, id := range []string{userID, userAgentID, systemID, systemAgentID} {
		var skillCount, fileCount int
		if err := env.db.QueryRow(t.Context(), `SELECT count(*) FROM skill WHERE id = $1`, id).Scan(&skillCount); err != nil {
			t.Fatalf("count deleted skill %s: %v", id, err)
		}
		if err := env.db.QueryRow(t.Context(), `SELECT count(*) FROM skill_file WHERE skill_id = $1`, id).Scan(&fileCount); err != nil {
			t.Fatalf("count deleted skill files %s: %v", id, err)
		}
		if skillCount != 0 || fileCount != 0 {
			t.Fatalf("deleted skill %s retained skill=%d files=%d", id, skillCount, fileCount)
		}
	}
}

// TestScopedSkills_ListScopeGuard verifies listing a system scope requires admin
// and that a scope returns only its own bucket.
func TestScopedSkills_ListScopeGuard(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "scoped-list")

	createScopedSkill(t, env, sid, map[string]any{"name": "mine", "scope": "user"})
	createTestSkill(t, env, "system", "", "", "global-sys")

	// Non-admin cannot list the system scope.
	rr := doRequestWithSession(t, env.srv, sid, "GET", "/api/skills?scope=system", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("non-admin system list status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	// Non-admin sees only their own user-scoped skills (default scope=user).
	rr = doRequestWithSession(t, env.srv, sid, "GET", "/api/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("user list status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	list := decodeSkillList(t, rr)
	if findSkill(list, "mine") == nil {
		t.Fatalf("user list missing own skill: %#v", list)
	}
	if findSkill(list, "global-sys") != nil {
		t.Fatalf("user list leaked system skill: %#v", list)
	}

	// Admin can list the system scope and sees the system skill.
	rr = doRequest(t, env, "GET", "/api/skills?scope=system", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin system list status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if findSkill(decodeSkillList(t, rr), "global-sys") == nil {
		t.Fatalf("admin system list missing system skill")
	}
}
