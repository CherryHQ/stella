package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
)

func TestListAuthUsers(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/users", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var users []struct {
		ID         int64    `json:"id"`
		Username   string   `json:"username"`
		IsActive   bool     `json:"is_active"`
		Roles      []string `json:"roles"`
		Identities []any    `json:"identities"`
		CreatedAt  string   `json:"created_at"`
	}
	if err := json.Unmarshal(resp.Data, &users); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(users) == 0 {
		t.Fatal("expected at least one user")
	}
	// The admin user created in setupAdmin should be present.
	found := false
	for _, u := range users {
		if u.Username == "testadmin" {
			found = true
			if !u.IsActive {
				t.Error("expected admin user to be active")
			}
			hasAdmin := false
			for _, r := range u.Roles {
				if r == "admin" {
					hasAdmin = true
				}
			}
			if !hasAdmin {
				t.Error("expected admin user to have admin role")
			}
		}
	}
	if !found {
		t.Error("expected to find testadmin user")
	}
}

func TestGetAuthUser(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/users/"+strconv.FormatInt(env.adminUser.ID, 10), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var u struct {
		ID       int64    `json:"id"`
		Username string   `json:"username"`
		Roles    []string `json:"roles"`
	}
	if err := json.Unmarshal(resp.Data, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.ID != env.adminUser.ID {
		t.Errorf("ID = %d, want %d", u.ID, env.adminUser.ID)
	}
	if u.Username != "testadmin" {
		t.Errorf("Username = %q, want %q", u.Username, "testadmin")
	}
}

func TestGetAuthUserNotFound(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/users/99999", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestUpdateAuthUserRolesAssignAdmin(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a regular user.
	hash, _ := auth.HashPassword("password1")
	user, err := env.authStore.CreateUser(ctx, "regular1", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = env.authStore.AssignRole(ctx, user.ID, auth.RoleUser)

	// Assign admin role.
	body := map[string]string{"role": "admin", "action": "assign"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+strconv.FormatInt(user.ID, 10)+"/roles", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify role assigned.
	roles, _ := env.authStore.ListUserRoles(ctx, user.ID)
	hasAdmin := false
	for _, r := range roles {
		if r.ID == auth.RoleAdmin {
			hasAdmin = true
		}
	}
	if !hasAdmin {
		t.Error("expected user to have admin role after assignment")
	}
}

func TestUpdateAuthUserRolesRemoveAdmin(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create another admin user.
	hash, _ := auth.HashPassword("password2")
	user, _ := env.authStore.CreateUser(ctx, "admin2", hash)
	_ = env.authStore.AssignRole(ctx, user.ID, auth.RoleAdmin)
	_ = env.authStore.AssignRole(ctx, user.ID, auth.RoleUser)

	// Remove admin role from the other admin (not self).
	body := map[string]string{"role": "admin", "action": "remove"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+strconv.FormatInt(user.ID, 10)+"/roles", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify role removed.
	roles, _ := env.authStore.ListUserRoles(ctx, user.ID)
	for _, r := range roles {
		if r.ID == auth.RoleAdmin {
			t.Error("expected admin role to be removed")
		}
	}
}

func TestCannotRemoveOwnAdminRole(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{"role": "admin", "action": "remove"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+strconv.FormatInt(env.adminUser.ID, 10)+"/roles", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	if resp.Error != "cannot remove your own admin role" {
		t.Errorf("error = %q, want %q", resp.Error, "cannot remove your own admin role")
	}
}

func TestUpdateAuthUserRolesInvalidAction(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{"role": "admin", "action": "invalid"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+strconv.FormatInt(env.adminUser.ID, 10)+"/roles", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestListAndUpdateAuthUserAgents(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a user.
	hash, _ := auth.HashPassword("password3")
	user, _ := env.authStore.CreateUser(ctx, "agentuser", hash)
	_ = env.authStore.AssignRole(ctx, user.ID, auth.RoleUser)
	uid := strconv.FormatInt(user.ID, 10)

	// List agents - initially empty.
	rr := doRequest(t, env, "GET", "/api/auth/users/"+uid+"/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", rr.Code, http.StatusOK)
	}
	resp := parseResponse(t, rr)
	var agentIDs []string
	_ = json.Unmarshal(resp.Data, &agentIDs)
	if len(agentIDs) != 0 {
		t.Errorf("expected 0 agents, got %d", len(agentIDs))
	}

	// Assign agent.
	body := map[string]any{"agent_ids": []string{"anna"}}
	rr = doRequest(t, env, "PUT", "/api/auth/users/"+uid+"/agents", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify assignment.
	rr = doRequest(t, env, "GET", "/api/auth/users/"+uid+"/agents", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &agentIDs)
	if len(agentIDs) != 1 || agentIDs[0] != "anna" {
		t.Errorf("expected [anna], got %v", agentIDs)
	}

	// Remove by setting empty.
	body = map[string]any{"agent_ids": []string{}}
	rr = doRequest(t, env, "PUT", "/api/auth/users/"+uid+"/agents", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Verify empty.
	rr = doRequest(t, env, "GET", "/api/auth/users/"+uid+"/agents", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &agentIDs)
	if len(agentIDs) != 0 {
		t.Errorf("expected 0 agents after removal, got %d", len(agentIDs))
	}
}

func TestUpdateAuthUserActive(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create a user.
	hash, _ := auth.HashPassword("password4")
	user, _ := env.authStore.CreateUser(ctx, "deactivateme", hash)
	_ = env.authStore.AssignRole(ctx, user.ID, auth.RoleUser)
	uid := strconv.FormatInt(user.ID, 10)

	// Create session for the user.
	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Deactivate.
	body := map[string]any{"is_active": false}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+uid+"/active", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify inactive.
	u, _ := env.authStore.GetUser(ctx, user.ID)
	if u.IsActive {
		t.Error("expected user to be inactive")
	}

	// Verify user session was deleted (force logout).
	_, err := env.authStore.GetSession(ctx, sessionID)
	if err == nil {
		t.Error("expected session to be deleted after deactivation")
	}

	// Reactivate.
	body = map[string]any{"is_active": true}
	rr = doRequest(t, env, "PUT", "/api/auth/users/"+uid+"/active", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	u, _ = env.authStore.GetUser(ctx, user.ID)
	if !u.IsActive {
		t.Error("expected user to be active after reactivation")
	}
}

func TestCannotDeactivateSelf(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]any{"is_active": false}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+strconv.FormatInt(env.adminUser.ID, 10)+"/active", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	if resp.Error != "cannot deactivate your own account" {
		t.Errorf("error = %q, want %q", resp.Error, "cannot deactivate your own account")
	}
}

func TestNonAdminCannotAccessAuthUserAPIs(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create non-admin user with session.
	hash, _ := auth.HashPassword("password5")
	user, _ := env.authStore.CreateUser(ctx, "nonadmin2", hash)
	_ = env.authStore.AssignRole(ctx, user.ID, auth.RoleUser)

	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    user.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// All auth user endpoints should be 403.
	endpoints := []struct {
		method string
		path   string
	}{
		{"GET", "/api/auth/users"},
		{"GET", "/api/auth/users/1"},
		{"PUT", "/api/auth/users/1/roles"},
		{"GET", "/api/auth/users/1/agents"},
		{"PUT", "/api/auth/users/1/agents"},
		{"PUT", "/api/auth/users/1/active"},
	}

	for _, ep := range endpoints {
		rr := doRequestWithSession(t, env.srv, sessionID, ep.method, ep.path, nil)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want %d", ep.method, ep.path, rr.Code, http.StatusForbidden)
		}
	}
}

func TestAuthUserWithLinkedIdentities(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Link an identity to the admin user.
	_, err := env.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     env.adminUser.ID,
		Platform:   "telegram",
		ExternalID: "tg123",
		Name:       "Test TG User",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	// Get user details — should include the identity.
	rr := doRequest(t, env, "GET", "/api/auth/users/"+strconv.FormatInt(env.adminUser.ID, 10), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var u struct {
		Identities []struct {
			Platform   string `json:"platform"`
			ExternalID string `json:"external_id"`
			Name       string `json:"name"`
		} `json:"identities"`
	}
	if err := json.Unmarshal(resp.Data, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(u.Identities) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(u.Identities))
	}
	if u.Identities[0].Platform != "telegram" {
		t.Errorf("platform = %q, want %q", u.Identities[0].Platform, "telegram")
	}
	if u.Identities[0].ExternalID != "tg123" {
		t.Errorf("external_id = %q, want %q", u.Identities[0].ExternalID, "tg123")
	}
}

func TestLegacyUsersAPIStillWorks(t *testing.T) {
	env := setupAdmin(t)

	// Old /api/users endpoint should still work.
	rr := doRequest(t, env, "GET", "/api/users", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}
