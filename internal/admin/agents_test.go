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

func TestAgentScopeInCreateAndGet(t *testing.T) {
	env := setupAdmin(t)

	// Create a restricted agent.
	body := config.Agent{
		ID:        "restricted-agent",
		Name:      "Restricted",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/restricted",
		Scope:     "restricted",
		Enabled:   true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Verify scope is persisted.
	rr = doRequest(t, env, "GET", "/api/agents/restricted-agent", nil)
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
		ID:        "anna",
		Name:      "Anna",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/anna",
		Scope:     "restricted",
		Enabled:   true,
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
		ID:        "bad-scope",
		Name:      "Bad",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/bad",
		Scope:     "invalid",
		Enabled:   true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestAgentUserAssignment(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a restricted agent.
	body := config.Agent{
		ID:        "secure",
		Name:      "Secure",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/secure",
		Scope:     "restricted",
		Enabled:   true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Create a user to assign.
	hash, _ := auth.HashPassword("userpassword")
	user, err := env.authStore.CreateUser(ctx, "testuser1", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// List users — initially empty.
	rr = doRequest(t, env, "GET", "/api/agents/secure/users", nil)
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
	rr = doRequest(t, env, "POST", "/api/agents/secure/users", map[string]any{"user_id": user.ID})
	if rr.Code != http.StatusOK {
		t.Fatalf("assign user: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify user appears in list.
	rr = doRequest(t, env, "GET", "/api/agents/secure/users", nil)
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
	rr = doRequest(t, env, "DELETE", "/api/agents/secure/users/"+strconv.FormatInt(user.ID, 10), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("remove user: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify user removed.
	rr = doRequest(t, env, "GET", "/api/agents/secure/users", nil)
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

	// Create a restricted agent.
	body := config.Agent{
		ID:        "private-agent",
		Name:      "Private",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/private",
		Scope:     "restricted",
		Enabled:   true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Create non-admin user.
	hash, _ := auth.HashPassword("userpassword")
	user, _ := env.authStore.CreateUser(ctx, "regular", hash)

	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Non-admin listing agents should see "anna" (system scope) but not "private-agent".
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents", nil)
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
		if a.ID == "private-agent" {
			foundPrivate = true
		}
	}
	if !foundAnna {
		t.Error("expected non-admin to see system-scoped 'anna' agent")
	}
	if foundPrivate {
		t.Error("non-admin should not see restricted 'private-agent'")
	}

	// Assign user to the restricted agent.
	_ = env.authStore.AssignAgent(ctx, user.ID, "private-agent")

	// Now listing should include the assigned agent.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &agents)

	foundPrivate = false
	for _, a := range agents {
		if a.ID == "private-agent" {
			foundPrivate = true
		}
	}
	if !foundPrivate {
		t.Error("expected assigned user to see restricted 'private-agent'")
	}
}

func TestNonAdminGetAgentAccessCheck(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a restricted agent.
	body := config.Agent{
		ID:        "secret",
		Name:      "Secret",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/secret",
		Scope:     "restricted",
		Enabled:   true,
	}
	rr := doRequest(t, env, "POST", "/api/agents", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create agent: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

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
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/anna", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get anna: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Non-admin cannot get restricted agent they're not assigned to.
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/secret", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("get secret: status = %d, want %d", rr.Code, http.StatusForbidden)
	}

	// Assign user, then they can access.
	_ = env.authStore.AssignAgent(ctx, user.ID, "secret")
	rr = doRequestWithSession(t, env.srv, sessionID, "GET", "/api/agents/secret", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("get secret after assign: status = %d, want %d", rr.Code, http.StatusOK)
	}
}
