package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

// findStellaID returns the seeded Stella agent's reserved ID.
func findStellaID(t *testing.T, env *testEnv) string {
	t.Helper()
	agents, err := env.store.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	for _, a := range agents {
		if a.Name == "Stella" {
			return a.ID
		}
	}
	t.Fatal("no Stella agent found")
	return ""
}

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

func TestListAgents_AdminVisibilityIsAuthorizedByPEP(t *testing.T) {
	env := setupAdmin(t)

	stellaID := findStellaID(t, env)
	restrictedID := createTestAgent(t, env, config.Agent{
		Name:    "Admin Hidden Restricted",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   config.AgentScopeRestricted,
		Enabled: true,
	})

	decodeAgents := func(path string) []config.Agent {
		t.Helper()
		rr := doRequest(t, env, http.MethodGet, path, nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%s", path, rr.Code, rr.Body.String())
		}
		items := parseListItems(t, rr, "agents")
		var agents []config.Agent
		if err := json.Unmarshal(items, &agents); err != nil {
			t.Fatalf("decode agents: %v", err)
		}
		return agents
	}
	contains := func(agents []config.Agent, id string) bool {
		for _, a := range agents {
			if a.ID == id {
				return true
			}
		}
		return false
	}

	regular := decodeAgents("/api/agents")
	if !contains(regular, stellaID) {
		t.Fatalf("regular agent list omitted system agent %s: %+v", stellaID, regular)
	}
	if !contains(regular, restrictedID) {
		t.Fatalf("regular agent list omitted PEP-authorized restricted agent %s", restrictedID)
	}

	management := decodeAgents("/api/agents?include_all=true")
	if !contains(management, restrictedID) {
		t.Fatalf("management agent list omitted restricted agent %s: %+v", restrictedID, management)
	}
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
	// A shared template must not hand every agent the same name: in a group,
	// two agents answering to "Stella" answer to each other's messages.
	if !strings.Contains(a.SystemPrompt, "Template-built") || strings.Contains(a.SystemPrompt, "Stella") {
		t.Errorf("SystemPrompt = %q, want the new agent's own name", a.SystemPrompt)
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

	stellaID := findStellaID(t, env)
	rr := doRequest(t, env, "GET", "/api/agents/"+stellaID, nil)
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
	rr := doRequest(t, env, "PATCH", "/api/agents/"+agentID, body)
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
	rr := doRequest(t, env, "PATCH", "/api/agents/"+agentID, body)
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
	rr := doRequest(t, env, "PATCH", "/api/agents/"+agentID, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestAgentUserAssignment(t *testing.T) {
	env := setupAdmin(t)

	agentID := createTestAgent(t, env, config.Agent{
		Name:    "Secure",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   "restricted",
		Enabled: true,
	})

	// Create a user to assign.
	user, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "testuser1", auth.RoleUser)

	// List users — initially empty.
	rr := doRequest(t, env, "GET", "/api/agents/"+agentID+"/users", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list users: status = %d (body: %s)", rr.Code, rr.Body.String())
	}
	var users []struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	_ = json.Unmarshal(parseListItems(t, rr, "users"), &users)
	if len(users) != 0 {
		t.Errorf("expected 0 users, got %d", len(users))
	}

	// Assign user.
	rr = doRequest(t, env, "POST", "/api/agents/"+agentID+"/users", map[string]any{"user_id": user.ID})
	if rr.Code != http.StatusCreated {
		t.Fatalf("assign user: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify user appears in list.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/users", nil)
	_ = json.Unmarshal(parseListItems(t, rr, "users"), &users)
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].ID != user.ID {
		t.Errorf("user ID = %q, want %q", users[0].ID, user.ID)
	}
	if users[0].Username != user.Email {
		t.Errorf("username = %q, want %q", users[0].Username, user.Email)
	}

	// Remove user.
	rr = doRequest(t, env, "DELETE", "/api/agents/"+agentID+"/users/"+user.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("remove user: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify user removed.
	rr = doRequest(t, env, "GET", "/api/agents/"+agentID+"/users", nil)
	_ = json.Unmarshal(parseListItems(t, rr, "users"), &users)
	if len(users) != 0 {
		t.Errorf("expected 0 users after removal, got %d", len(users))
	}
}

// TestAgentUserAssignmentNonAdminDenied, TestNonAdminSeesOnlyAccessibleAgents,
// TestNonAdminGetAgentAccessCheck removed: single-tenant mode grants admin to all
// authenticated users, so non-admin RBAC is not exercised.
