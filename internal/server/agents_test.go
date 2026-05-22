package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

// createTestAgent creates an agent via POST and returns its auto-generated ID.
func createTestAgent(t *testing.T, env *testEnv, a config.Agent) string {
	t.Helper()
	rr := doRequest(t, env, "POST", "/api/agents", a)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent %q: status = %d (body: %s)", a.Name, rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var created config.Agent
	if err := json.Unmarshal(resp.Data, &created); err != nil {
		t.Fatalf("unmarshal created agent: %v", err)
	}
	return created.ID
}

func TestCreateAgentFromTemplate(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{
		"name":        "Template-built",
		"template_id": "stella",
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var a config.Agent
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.SystemPrompt == "" {
		t.Errorf("expected SystemPrompt populated from template body, got empty")
	}
}

func TestCreateAgentUserOverridesTemplate(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{
		"name":          "Override-me",
		"template_id":   "stella",
		"system_prompt": "CUSTOM PROMPT",
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var a config.Agent
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.SystemPrompt != "CUSTOM PROMPT" {
		t.Errorf("SystemPrompt overridden = %q, want CUSTOM PROMPT", a.SystemPrompt)
	}
}

func TestAgentScopeInCreateAndGet(t *testing.T) {
	env := setupAdmin(t)

	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Restricted Agent",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	})

	// Verify scope is persisted.
	rr := doRequest(t, env, "GET", "/api/agents/"+agentID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", rr.Code, http.StatusOK)
	}
	resp := parseResponse(t, rr)
	var a config.Agent
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.Scope != "restricted" {
		t.Errorf("Scope = %q, want %q", a.Scope, "restricted")
	}
}

func TestAgentScopeDefaultsToSystem(t *testing.T) {
	env := setupAdmin(t)

	// The seeded "stella" agent should have system scope.
	rr := doRequest(t, env, "GET", "/api/agents/stella", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d", rr.Code, http.StatusOK)
	}
	resp := parseResponse(t, rr)
	var a config.Agent
	if err := json.Unmarshal(resp.Data, &a); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if a.Scope != "system" {
		t.Errorf("Scope = %q, want %q", a.Scope, "system")
	}
}

func TestAgentScopeInUpdate(t *testing.T) {
	env := setupAdmin(t)

	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Updatable Scope",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	})

	body := config.Agent{
		Name:    "Updatable Scope",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "system",
		Enabled: true,
	}
	rr := doRequest(t, env, "PUT", "/api/agents/"+agentID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify scope persisted.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID, nil)
	resp := parseResponse(t, rr)
	var a config.Agent
	_ = json.Unmarshal(resp.Data, &a)
	if a.Scope != "system" {
		t.Errorf("Scope = %q, want %q", a.Scope, "system")
	}
}

func TestAdminCanUpdateAgentCreatedByAnotherUser(t *testing.T) {
	env := setupAdmin(t)

	_, creatorSID := newNonAdmin(t, env, "agent-owner")
	agentID := createAgentAsUser(t, env, creatorSID, "owned-agent")

	body := config.Agent{
		Name:    "Admin Edited",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "system",
		Enabled: true,
	}
	rr := doRequest(t, env, "PUT", "/api/agents/"+agentID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("admin update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var updated config.Agent
	if err := json.Unmarshal(resp.Data, &updated); err != nil {
		t.Fatalf("unmarshal updated agent: %v", err)
	}
	if updated.Name != "Admin Edited" {
		t.Fatalf("updated name = %q, want %q", updated.Name, "Admin Edited")
	}
	if updated.Scope != "system" {
		t.Fatalf("updated scope = %q, want %q", updated.Scope, "system")
	}
}

func TestAgentInvalidScope(t *testing.T) {
	env := setupAdmin(t)

	body := config.Agent{
		Name:    "Bad Scope",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "invalid",
		Enabled: true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestAgentInvalidSandbox(t *testing.T) {
	env := setupAdmin(t)

	body := config.Agent{
		Name:    "Bad Sandbox",
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
		Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: "bogus"}},
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestAgentInvalidSandboxOnUpdate(t *testing.T) {
	env := setupAdmin(t)

	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Sandbox Update",
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
	})

	body := config.Agent{
		Name:    "Sandbox Update",
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
		Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: "bogus"}},
	}
	rr := doRequest(t, env, "PUT", "/api/agents/"+agentID, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestAgentUserAssignment(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Secure",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	})

	// Create a user to assign.
	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(ctx, "testuser1", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// List users — initially empty.
	rr := doRequest(t, env, "GET", "/api/agents/"+agentID+"/users", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list users: status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	_ = json.Unmarshal(resp.Data, &users)
	if len(users) != 0 {
		t.Errorf("expected 0 users, got %d", len(users))
	}

	// Assign user.
	rr = doRequest(t, env, "POST", "/api/agents/"+agentID+"/users", map[string]any{"user_id": user.ID})
	if rr.Code != http.StatusOK {
		t.Fatalf("assign user: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify user appears in list.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/users", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &users)
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].ID != user.ID {
		t.Errorf("user ID = %q, want %q", users[0].ID, user.ID)
	}
	if users[0].Username != "testuser1" {
		t.Errorf("username = %q, want %q", users[0].Username, "testuser1")
	}

	// Remove user.
	rr = doRequest(t, env, "DELETE", "/api/agents/"+agentID+"/users/"+user.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove user: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify user removed.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/users", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &users)
	if len(users) != 0 {
		t.Errorf("expected 0 users after removal, got %d", len(users))
	}
}

func TestAgentUserAssignmentNonAdminDenied(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create non-admin user session.
	hash, _ := auth.HashPassword("userpassword")
	user, _ := env.authStore.CreateUser(ctx, "nonadmin", hash)

	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Non-admin cannot access agent user APIs.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/stella/users", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestNonAdminSeesOnlyAccessibleAgents(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a restricted agent (admin creates it).
	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Private",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	})

	// Create non-admin user.
	hash, _ := auth.HashPassword("userpassword")
	user, _ := env.authStore.CreateUser(ctx, "regular", hash)

	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Non-admin listing agents should see "stella" (system scope) but not the restricted agent.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list agents: status = %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var agents []config.Agent
	_ = json.Unmarshal(resp.Data, &agents)

	foundStella := false
	foundPrivate := false
	for _, a := range agents {
		if a.ID == "stella" {
			foundStella = true
		}
		if a.ID == agentID {
			foundPrivate = true
		}
	}
	if !foundStella {
		t.Error("expected non-admin to see system-scoped 'stella' agent")
	}
	if foundPrivate {
		t.Error("non-admin should not see restricted agent")
	}

	// Assign user to the restricted agent.
	_ = env.authStore.AssignAgent(ctx, user.ID, agentID)

	// Now listing should include the assigned agent.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &agents)

	foundPrivate = false
	for _, a := range agents {
		if a.ID == agentID {
			foundPrivate = true
		}
	}
	if !foundPrivate {
		t.Error("expected assigned user to see restricted agent")
	}
}

func TestNonAdminGetAgentAccessCheck(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a restricted agent.
	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Secret",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	})

	// Create non-admin user.
	hash, _ := auth.HashPassword("userpassword")
	user, _ := env.authStore.CreateUser(ctx, "regular2", hash)

	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Non-admin can get system agent.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/stella", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get stella: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Non-admin cannot get restricted agent they're not assigned to.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/"+agentID, nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("get secret: status = %d, want %d", rr.Code, http.StatusForbidden)
	}

	// Assign user, then they can access.
	_ = env.authStore.AssignAgent(ctx, user.ID, agentID)
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/"+agentID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get secret after assign: status = %d, want %d", rr.Code, http.StatusOK)
	}
}
