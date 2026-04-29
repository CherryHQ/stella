package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
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
		"template_id": "anna",
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
		"template_id":   "anna",
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

	// The seeded "anna" agent should have system scope.
	rr := doRequest(t, env, "GET", "/api/agents/anna", nil)
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

	// Update anna to restricted scope.
	body := config.Agent{
		Name:    "Anna",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	}
	rr := doRequest(t, env, "PUT", "/api/agents/anna", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify scope persisted.
	rr = doRequest(t, env, "GET", "/api/agents/anna", nil)
	resp := parseResponse(t, rr)
	var a config.Agent
	_ = json.Unmarshal(resp.Data, &a)
	if a.Scope != "restricted" {
		t.Errorf("Scope = %q, want %q", a.Scope, "restricted")
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

	body := config.Agent{
		Name:    "Anna",
		Model:   "anthropic/claude-sonnet-4-6",
		Enabled: true,
		Sandbox: config.SandboxConfig{Network: config.SandboxNetworkConfig{Mode: "bogus"}},
	}
	rr := doRequest(t, env, "PUT", "/api/agents/anna", body)
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
		ID       int64  `json:"id"`
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
		t.Errorf("user ID = %d, want %d", users[0].ID, user.ID)
	}
	if users[0].Username != "testuser1" {
		t.Errorf("username = %q, want %q", users[0].Username, "testuser1")
	}

	// Remove user.
	rr = doRequest(t, env, "DELETE", "/api/agents/"+agentID+"/users/"+strconv.FormatInt(user.ID, 10), nil)
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
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/anna/users", nil)
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

	// Non-admin listing agents should see "anna" (system scope) but not the restricted agent.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list agents: status = %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var agents []config.Agent
	_ = json.Unmarshal(resp.Data, &agents)

	foundAnna := false
	foundPrivate := false
	for _, a := range agents {
		if a.ID == "anna" {
			foundAnna = true
		}
		if a.ID == agentID {
			foundPrivate = true
		}
	}
	if !foundAnna {
		t.Error("expected non-admin to see system-scoped 'anna' agent")
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
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/anna", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get anna: status = %d, want %d", rr.Code, http.StatusOK)
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
