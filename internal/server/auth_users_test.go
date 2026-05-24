package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func TestListAuthUsers(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/users", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var users []struct {
		ID         string `json:"id"`
		Username   string `json:"username"`
		IsActive   bool   `json:"is_active"`
		Role       string `json:"role"`
		Identities []any  `json:"identities"`
		CreatedAt  string `json:"created_at"`
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
			if u.Role != "admin" {
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

	rr := doRequest(t, env, "GET", "/api/auth/users/"+env.adminUser.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var u struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
	}
	if err := json.Unmarshal(resp.Data, &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.ID != env.adminUser.ID {
		t.Errorf("ID = %q, want %q", u.ID, env.adminUser.ID)
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

func TestUpdateAuthUserRolePromote(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("password1")
	user, err := env.authStore.CreateUser(ctx, "regular1", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	body := map[string]string{"role": "admin"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+user.ID+"/role", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	got, _ := env.authStore.GetUser(ctx, user.ID)
	if got.Role != auth.RoleAdmin {
		t.Errorf("role = %q, want %q", got.Role, auth.RoleAdmin)
	}
}

func TestUpdateAuthUserRoleDemote(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("password2")
	user, _ := env.authStore.CreateUser(ctx, "admin2", hash)
	_ = env.authStore.UpdateUserRole(ctx, user.ID, auth.RoleAdmin)

	body := map[string]string{"role": "user"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+user.ID+"/role", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	got, _ := env.authStore.GetUser(ctx, user.ID)
	if got.Role != auth.RoleUser {
		t.Errorf("role = %q, want %q", got.Role, auth.RoleUser)
	}
}

func TestCannotDemoteSelf(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{"role": "user"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+env.adminUser.ID+"/role", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestUpdateAuthUserRoleInvalid(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{"role": "superadmin"}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+env.adminUser.ID+"/role", body)
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
	uid := user.ID

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
	body := map[string]any{"agent_ids": []string{"stella"}}
	rr = doRequest(t, env, "PUT", "/api/auth/users/"+uid+"/agents", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("update status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify assignment.
	rr = doRequest(t, env, "GET", "/api/auth/users/"+uid+"/agents", nil)
	resp = parseResponse(t, rr)
	_ = json.Unmarshal(resp.Data, &agentIDs)
	if len(agentIDs) != 1 || agentIDs[0] != "stella" {
		t.Errorf("expected [stella], got %v", agentIDs)
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
	uid := user.ID

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
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+env.adminUser.ID+"/active", body)
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
		{"PUT", "/api/auth/users/1/role"},
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

// --- Phase 3: login identity admin API tests ---

func setupOIDCStore(t *testing.T, env *testEnv) *appdb.OIDCStore {
	t.Helper()
	store := appdb.NewOIDCStore(env.db)
	env.srv.SetLoginIdentityStore(store)
	return store
}

func TestListAuthUserLoginIdentitiesEmpty(t *testing.T) {
	env := setupAdmin(t)
	setupOIDCStore(t, env)

	rr := doRequest(t, env, "GET", "/api/auth/users/"+env.adminUser.ID+"/identities/login", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var identities []any
	_ = json.Unmarshal(resp.Data, &identities)
	if len(identities) != 0 {
		t.Errorf("expected 0 login identities, got %d", len(identities))
	}
}

func TestListAuthUserLoginIdentitiesUserNotFound(t *testing.T) {
	env := setupAdmin(t)
	setupOIDCStore(t, env)

	rr := doRequest(t, env, "GET", "/api/auth/users/nonexistent/identities/login", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestLinkAuthUserLoginIdentity(t *testing.T) {
	env := setupAdmin(t)
	store := setupOIDCStore(t, env)
	ctx := context.Background()

	// First ensure auth_user exists (local OIDC backfill path).
	_, err := store.CreateUser(ctx, auth.User{
		ID:    env.adminUser.ID,
		Email: "testadmin@example.com",
		Name:  "Test Admin",
	})
	// Ignore duplicate error; admin user may already exist.
	_ = err

	body := map[string]string{
		"provider":         "local",
		"provider_subject": env.adminUser.ID,
		"email":            "testadmin@example.com",
		"name":             "Test Admin",
	}
	rr := doRequest(t, env, "POST", "/api/auth/users/"+env.adminUser.ID+"/identities/login", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var identity struct {
		Provider        string `json:"provider"`
		ProviderSubject string `json:"provider_subject"`
		Email           string `json:"email"`
		UserId          string `json:"user_id"`
	}
	if err := json.Unmarshal(resp.Data, &identity); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if identity.Provider != "local" {
		t.Errorf("provider = %q, want %q", identity.Provider, "local")
	}
	if identity.Email != "testadmin@example.com" {
		t.Errorf("email = %q, want %q", identity.Email, "testadmin@example.com")
	}
	if identity.UserId != env.adminUser.ID {
		t.Errorf("user_id = %q, want %q", identity.UserId, env.adminUser.ID)
	}
}

func TestLinkLoginIdentityOwnedByAnotherUserIsConflict(t *testing.T) {
	env := setupAdmin(t)
	store := setupOIDCStore(t, env)
	ctx := context.Background()

	// Create two users in both tables (legacy auth_users + new auth_user).
	hash, _ := auth.HashPassword("pw")
	u1, _ := env.authStore.CreateUser(ctx, "linktest1-"+uuid.NewString(), hash)
	u2, _ := env.authStore.CreateUser(ctx, "linktest2-"+uuid.NewString(), hash)
	for _, u := range []auth.AuthUser{u1, u2} {
		_, _ = store.CreateUser(ctx, auth.User{ID: u.ID, Email: u.Username + "@test.example", Name: u.Username})
	}

	// Link the identity to u1.
	_, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{
		ID:              uuid.NewString(),
		UserID:          u1.ID,
		Provider:        "oidc",
		ProviderSubject: "sub-abc",
		Email:           "shared@example.com",
	})
	if err != nil {
		t.Fatalf("CreateLoginIdentity: %v", err)
	}

	// Attempt to link the same identity to u2 — should conflict.
	body := map[string]string{
		"provider":         "oidc",
		"provider_subject": "sub-abc",
		"email":            "shared@example.com",
	}
	rr := doRequest(t, env, "POST", "/api/auth/users/"+u2.ID+"/identities/login", body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusConflict, rr.Body.String())
	}
}

func TestLinkLoginIdentityIdempotent(t *testing.T) {
	env := setupAdmin(t)
	store := setupOIDCStore(t, env)
	ctx := context.Background()

	hash, _ := auth.HashPassword("pw")
	u, _ := env.authStore.CreateUser(ctx, "idemuser-"+uuid.NewString(), hash)
	// Seed auth_user so the FK on auth_identity is satisfied.
	_, _ = store.CreateUser(ctx, auth.User{ID: u.ID, Email: u.Username + "@test.example", Name: u.Username})

	// Link once.
	_, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{
		ID:              uuid.NewString(),
		UserID:          u.ID,
		Provider:        "local",
		ProviderSubject: u.ID,
		Email:           u.Username + "@test.example",
	})
	if err != nil {
		t.Fatalf("CreateLoginIdentity: %v", err)
	}

	// Linking again for the same user should return 200 (idempotent).
	body := map[string]string{
		"provider":         "local",
		"provider_subject": u.ID,
		"email":            u.Username + "@test.example",
	}
	rr := doRequest(t, env, "POST", "/api/auth/users/"+u.ID+"/identities/login", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestListAuthUserChannelIdentities(t *testing.T) {
	env := setupAdmin(t)
	setupOIDCStore(t, env)
	ctx := context.Background()

	// Add a channel identity to the admin user.
	_, err := env.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     env.adminUser.ID,
		Platform:   "telegram",
		ExternalID: "tg-555",
		Name:       "TG User",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/auth/users/"+env.adminUser.ID+"/identities/channel", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var identities []struct {
		Platform   string `json:"platform"`
		ExternalID string `json:"external_id"`
	}
	if err := json.Unmarshal(resp.Data, &identities); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(identities) != 1 || identities[0].Platform != "telegram" {
		t.Errorf("unexpected identities: %v", identities)
	}
}

func TestListAuthUserChannelIdentitiesUserNotFound(t *testing.T) {
	env := setupAdmin(t)
	setupOIDCStore(t, env)

	rr := doRequest(t, env, "GET", "/api/auth/users/nonexistent/identities/channel", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// --- Phase 4: membership-authoritative role/active tests ---

func setupMembershipStore(t *testing.T, env *testEnv) *appdb.OIDCStore {
	t.Helper()
	store := appdb.NewOIDCStore(env.db)
	env.srv.SetLoginIdentityStore(store)
	env.srv.SetMembershipStore(store)
	return store
}

// createUserWithMembership creates a legacy user in auth_users, a matching
// auth_user record, and an auth_membership. Returns the legacy user and membership ID.
func createUserWithMembership(t *testing.T, env *testEnv, store *appdb.OIDCStore, username, role string) (auth.AuthUser, auth.Membership) {
	t.Helper()
	ctx := context.Background()
	hash, _ := auth.HashPassword("pw")
	u, err := env.authStore.CreateUser(ctx, username, hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	_ = env.authStore.UpdateUserRole(ctx, u.ID, role)

	_, _ = store.CreateUser(ctx, auth.User{
		ID:    u.ID,
		Email: username + "@test.example",
		Name:  username,
	})
	org, _ := store.CreateOrganization(ctx, auth.Organization{
		ID:   uuid.NewString(),
		Name: username + "-org",
	})
	m, err := store.CreateMembership(ctx, auth.Membership{
		ID:             uuid.NewString(),
		UserID:         u.ID,
		OrganizationID: org.ID,
		Role:           role,
		IsActive:       true,
	})
	if err != nil {
		t.Fatalf("CreateMembership: %v", err)
	}
	// Re-read user to get latest state.
	u2, _ := env.authStore.GetUser(ctx, u.ID)
	return u2, m
}

func TestRoleDowngradeReflectsInMembership(t *testing.T) {
	env := setupAdmin(t)
	store := setupMembershipStore(t, env)
	ctx := context.Background()

	u, m := createUserWithMembership(t, env, store, "roletest-"+uuid.NewString(), auth.RoleAdmin)
	if m.Role != auth.RoleAdmin {
		t.Fatalf("membership role = %q, want %q", m.Role, auth.RoleAdmin)
	}

	// Demote to user via admin API.
	body := map[string]string{"role": auth.RoleUser}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+u.ID+"/role", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Membership should now reflect user role.
	updated, err := store.GetUserMembership(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUserMembership: %v", err)
	}
	if updated.Role != auth.RoleUser {
		t.Errorf("membership role = %q, want %q", updated.Role, auth.RoleUser)
	}
}

func TestInactiveMembershipBlocksLegacySession(t *testing.T) {
	env := setupAdmin(t)
	store := setupMembershipStore(t, env)
	ctx := context.Background()

	u, m := createUserWithMembership(t, env, store, "inactivetest-"+uuid.NewString(), auth.RoleUser)
	_ = m

	// Create a session for the user.
	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Deactivate via admin API.
	body := map[string]any{"is_active": false}
	rr := doRequest(t, env, "PUT", "/api/auth/users/"+u.ID+"/active", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("deactivate status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Membership should be inactive.
	updated, err := store.GetUserMembership(ctx, u.ID)
	if err != nil {
		t.Fatalf("GetUserMembership: %v", err)
	}
	if updated.IsActive {
		t.Error("membership should be inactive after deactivation")
	}

	// A new session with the same user should be blocked by membership check.
	newSessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        newSessionID,
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})
	rr = doRequestWithSession(t, env.srv, newSessionID, "GET", "/api/auth/users", nil)
	// Should be denied because membership is inactive.
	if rr.Code == http.StatusOK {
		t.Error("expected denied access for inactive membership user, got 200")
	}
}

func TestMembershipRoleOverridesLegacyRole(t *testing.T) {
	env := setupAdmin(t)
	store := setupMembershipStore(t, env)
	ctx := context.Background()

	u, _ := createUserWithMembership(t, env, store, "roleoverride-"+uuid.NewString(), auth.RoleUser)

	// Create a session for the user.
	sessionID := auth.NewSessionID()
	_, _ = env.authStore.CreateSession(ctx, auth.Session{
		ID:        sessionID,
		UserID:    u.ID,
		ExpiresAt: time.Now().Add(auth.SessionDuration),
	})

	// Attempting admin endpoint with user membership should be forbidden.
	rr := doRequestWithSession(t, env.srv, sessionID, "GET", "/api/auth/users", nil)
	if rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d (body: %s)", rr.Code, rr.Body.String())
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
	rr := doRequest(t, env, "GET", "/api/auth/users/"+env.adminUser.ID, nil)
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
