package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/skills"
)

// newNonAdmin creates a non-admin user with an active session and returns
// (user, session id).
func newNonAdmin(t *testing.T, env *testEnv, username string) (auth.AuthUser, string) {
	t.Helper()
	hash, _ := auth.HashPassword("password")
	u, err := env.authStore.CreateUser(context.Background(), username, hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	sid := auth.NewSessionID()
	if _, err := env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        sid,
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return u, sid
}

// createAgentAsUser creates an agent via the API using the given session
// (so CreatorID is set to that session's user). Returns the agent ID.
func createAgentAsUser(t *testing.T, env *testEnv, sessionID, name string) string {
	t.Helper()
	rr := doRequestWithSession(t, env.srv, sessionID, "POST", "/api/agents", config.Agent{
		Name:    name,
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent %q: status = %d (body: %s)", name, rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created config.Agent
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return created.ID
}

// createTestSkill creates a skill via the store and returns its ID.
func createTestSkill(t *testing.T, env *testEnv, scope string, userID int64, agentID, name string) string {
	t.Helper()
	sk := skills.Skill{
		Scope:       scope,
		UserID:      userID,
		AgentID:     agentID,
		Name:        name,
		Description: "test",
		Status:      "active",
	}
	id, err := env.pluginHost.SkillStore().Create(context.Background(), sk, map[string]string{
		skills.MainFile: "# " + name,
		"reference.md":  "reference content",
	})
	if err != nil {
		t.Fatalf("Create skill: %v", err)
	}
	return id
}

// --- Agent-scoped endpoints ---

func TestAgentSkills_ListCreatorAccess(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "creator-list")
	_, otherSID := newNonAdmin(t, env, "other-list")

	agentID := createAgentAsUser(t, env, creatorSID, "list-agent")
	createTestSkill(t, env, "agent", 0, agentID, "agent-skill-1")

	// Creator: 200 + 1 skill.
	rr := doRequestWithSession(t, env.srv, creatorSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("creator status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 1 {
		t.Errorf("creator: got %d skills, want 1", len(list))
	}

	// Another user: 403.
	rr = doRequestWithSession(t, env.srv, otherSID, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("other status = %d, want 403 (body: %s)", rr.Code, rr.Body.String())
	}

	// Admin: 200.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Unauth: 401.
	rr = doUnauthRequest(t, env.srv, "GET", "/api/agents/"+agentID+"/skills", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", rr.Code)
	}
}

func TestAgentSkills_CrossAgentScope(t *testing.T) {
	env := setupAdmin(t)

	_, sid := newNonAdmin(t, env, "creator-cross")
	a1 := createAgentAsUser(t, env, sid, "cross-a1")
	a2 := createAgentAsUser(t, env, sid, "cross-a2")

	skID := createTestSkill(t, env, "agent", 0, a1, "skill-on-agent1")

	// GET /agents/{a2}/skills/{skID} must 404 — skill belongs to a1.
	rr := doRequestWithSession(t, env.srv, sid, "GET", "/api/agents/"+a2+"/skills/"+skID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-agent get status = %d, want 404 (body: %s)", rr.Code, rr.Body.String())
	}
}

func TestAgentSkills_UpdateDeleteFile(t *testing.T) {
	env := setupAdmin(t)

	_, sid := newNonAdmin(t, env, "creator-ud")
	agentID := createAgentAsUser(t, env, sid, "ud-agent")
	skID := createTestSkill(t, env, "agent", 0, agentID, "skill-ud")

	// Update description.
	desc := "updated"
	rr := doRequestWithSession(t, env.srv, sid, "PUT", "/api/agents/"+agentID+"/skills/"+skID, map[string]any{
		"description": desc,
		"files":       map[string]string{"SKILL.md": "# updated body"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Delete the ref file.
	rr = doRequestWithSession(t, env.srv, sid, "DELETE", "/api/agents/"+agentID+"/skills/"+skID+"/file?path=reference.md", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete file status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Cannot delete SKILL.md.
	rr = doRequestWithSession(t, env.srv, sid, "DELETE", "/api/agents/"+agentID+"/skills/"+skID+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete SKILL.md status = %d, want 400", rr.Code)
	}
}

// --- Profile (self-user) endpoints ---

func TestProfileSkills_SelfOnly(t *testing.T) {
	env := setupAdmin(t)

	u1, sid1 := newNonAdmin(t, env, "user1")
	u2, sid2 := newNonAdmin(t, env, "user2")

	skID1 := createTestSkill(t, env, "user", u1.ID, "", "u1-skill")
	_ = createTestSkill(t, env, "user", u2.ID, "", "u2-skill")

	// u1 sees only their own skill.
	rr := doRequestWithSession(t, env.srv, sid1, "GET", "/api/auth/profile/skills", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("u1 list status = %d, want 200", rr.Code)
	}
	resp := parseResponse(t, rr)
	var list []map[string]any
	_ = json.Unmarshal(resp.Data, &list)
	if len(list) != 1 {
		t.Errorf("u1 list: got %d, want 1", len(list))
	}

	// u2 cannot GET u1's skill.
	rr = doRequestWithSession(t, env.srv, sid2, "GET", "/api/auth/profile/skills/"+skID1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("u2 cross status = %d, want 404", rr.Code)
	}

	// u2 cannot DELETE u1's skill.
	rr = doRequestWithSession(t, env.srv, sid2, "DELETE", "/api/auth/profile/skills/"+skID1, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("u2 cross delete status = %d, want 404", rr.Code)
	}
}

func TestAdminDeleteSkillFile(t *testing.T) {
	env := setupAdmin(t)

	skID := createTestSkill(t, env, "system", 0, "", "sys-skill")

	rr := doRequest(t, env, "DELETE", "/api/skills/"+skID+"/file?path=reference.md", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete ref status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// SKILL.md rejected.
	rr = doRequest(t, env, "DELETE", "/api/skills/"+skID+"/file?path=SKILL.md", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("delete SKILL.md status = %d, want 400", rr.Code)
	}
}
