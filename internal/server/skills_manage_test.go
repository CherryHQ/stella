package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/authz/policy"
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

// TestScopedSkills_ListScopeGuard verifies listing a system scope requires admin
// and that a scope returns only its own bucket.
func TestScopedSkills_ListHonorsPerRowPolicy(t *testing.T) {
	env := setupAdmin(t)
	_, sid := newNonAdmin(t, env, "scoped-policy")
	visibleID := createScopedSkill(t, env, sid, map[string]any{"name": "visible", "scope": "user"})
	hiddenID := createScopedSkill(t, env, sid, map[string]any{"name": "hidden", "scope": "user"})
	if _, err := env.db.Exec(context.Background(), `UPDATE skill SET metadata = '{"source":"tainted"}'::jsonb WHERE id = $1`, hiddenID); err != nil {
		t.Fatal(err)
	}
	ps := policy.NewService(policy.New(env.db))
	if _, _, err := ps.CreatePolicy(context.Background(), policy.PolicyInput{
		Name: "hide tainted skill", Resource: authz.ResourceSkill, Action: authz.ActionRead,
		Effect: policy.EffectDeny, Subjects: policy.AnySubject(),
		Predicates: []policy.Predicate{policy.Eq("source", "tainted")},
	}); err != nil {
		t.Fatal(err)
	}

	rr := doRequestWithSession(t, env.srv, sid, "GET", "/api/skills?scope=user", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	list := decodeSkillList(t, rr)
	if findSkill(list, visibleID) == nil && findSkill(list, "visible") == nil {
		t.Fatalf("visible skill missing: %#v", list)
	}
	if findSkill(list, hiddenID) != nil || findSkill(list, "hidden") != nil {
		t.Fatalf("custom-denied skill leaked: %#v", list)
	}
}

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
