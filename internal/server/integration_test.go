package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/server"
)

// --- Integration tests: cross-cutting auth & lifecycle flows ---

func TestFirstUserGetsAdmin(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)

	// ProcessOIDCLogin with a brand-new email.
	// The template DB already has the admin from setupAdmin, so this is the
	// second user — should get "user" role.
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	result, err := svc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider:  "test",
		Subject:   "sub-second",
		Email:     "second@test.local",
		Name:      "Second User",
		AvatarURL: "",
	}, sessionMgr)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}
	if result.User.Role != auth.RoleUser {
		t.Errorf("second user role = %q, want %q", result.User.Role, auth.RoleUser)
	}
	if !result.IsNewUser {
		t.Error("expected second user to be flagged as new")
	}
}

func TestFirstUserGetsAdmin_EmptyDB(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Delete ALL users so the next login is truly the "first user".
	users, err := env.oidcStore.ListUsers(ctx)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	for _, u := range users {
		if err := env.oidcStore.DeleteUser(ctx, u.ID); err != nil {
			t.Fatalf("DeleteUser %q: %v", u.ID, err)
		}
	}

	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	result, err := svc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "test",
		Subject:  "sub-first",
		Email:    "first@test.local",
		Name:     "First User",
	}, sessionMgr)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}
	if result.User.Role != auth.RoleAdmin {
		t.Errorf("first user role = %q, want %q", result.User.Role, auth.RoleAdmin)
	}
}

func TestExternalIdentityDoesNotAutoLinkByEmail(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	existing, err := env.oidcStore.CreateUser(ctx, auth.User{
		ID:       uuid.NewString(),
		Email:    "same@test.local",
		Name:     "Existing",
		Role:     auth.RoleAdmin,
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}

	_, err = svc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "oauth",
		Subject:  "attacker-subject",
		Email:    existing.Email,
		Name:     "Attacker",
	}, sessionMgr)
	if err == nil {
		t.Fatal("expected duplicate email to fail instead of linking to existing user")
	}
	if _, err := env.oidcStore.GetLoginIdentityByProvider(ctx, "oauth", "attacker-subject"); err == nil {
		t.Fatal("unexpected login identity created for duplicate email")
	}
}

func TestLocalLoginURLWorksWithoutExternalProviders(t *testing.T) {
	env := setupAdmin(t)
	env.rebuild(t, func(d *server.Deps) {
		d.OIDC.LocalAuth = local.NewService(&local.Config{BootstrapRegistration: true}, env.oidcStore, env.oidcStore)
	})

	rr := doUnauthRequest(t, env.srv, http.MethodGet, "/auth/login/local", nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusFound, rr.Body.String())
	}
	if got := rr.Header().Get("Location"); got != "/login" {
		t.Fatalf("Location = %q, want /login", got)
	}
}

func TestCreateSessionForUserDoesNotCreateLocalLoginIdentity(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	user, err := env.oidcStore.CreateUser(ctx, auth.User{
		ID:       uuid.NewString(),
		Email:    "local@test.local",
		Name:     "Local User",
		Role:     auth.RoleUser,
		IsActive: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	result, err := svc.CreateSessionForUser(ctx, user.ID, sessionMgr)
	if err != nil {
		t.Fatalf("CreateSessionForUser: %v", err)
	}
	if result.User.ID != user.ID || result.SessionToken == "" {
		t.Fatalf("bad session result: %+v", result)
	}
	if identities, err := env.oidcStore.ListLoginIdentitiesByUser(ctx, user.ID); err != nil {
		t.Fatalf("ListLoginIdentitiesByUser: %v", err)
	} else if len(identities) != 0 {
		t.Fatalf("unexpected login identities: %+v", identities)
	}
}

func TestDeactivatedUserBlockedOnBearerAuth(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	user, token := createTestUserWithToken(t, env.authStore, env.oidcStore, "victim", auth.RoleUser)

	// Bearer auth should work initially.
	rr := doBearerRequest(t, env.srv, token, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("active user: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Deactivate the user.
	if err := env.oidcStore.UpdateUserActive(ctx, user.ID, false); err != nil {
		t.Fatalf("UpdateUserActive: %v", err)
	}

	// Bearer auth should now be blocked.
	rr = doBearerRequest(t, env.srv, token, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("deactivated user: status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestDeactivatedUserBlockedOnSessionAuth(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Set up OIDC auth components so session-based auth works.
	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	env.rebuild(t, func(d *server.Deps) {
		d.OIDC.AuthSvc = svc
		d.OIDC.SessionMgr = sessionMgr
	})

	// Create a user via OIDC login flow — this produces a session token.
	result, err := svc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "test",
		Subject:  "sub-session-victim",
		Email:    "sessionvictim@test.local",
		Name:     "Session Victim",
	}, sessionMgr)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}

	// Session auth should work initially.
	rr := doSessionRequest(t, env.srv, result.SessionToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("active session user: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Deactivate the user.
	if err := env.oidcStore.UpdateUserActive(ctx, result.User.ID, false); err != nil {
		t.Fatalf("UpdateUserActive: %v", err)
	}

	// Session auth should now be blocked (PrincipalFromToken checks IsActive).
	rr = doSessionRequest(t, env.srv, result.SessionToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("deactivated session user: status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestNonAdminCannotAccessAdminEndpoints(t *testing.T) {
	env := setupAdmin(t)

	user, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "regular", auth.RoleUser)

	rr := doBearerRequest(t, env.srv, userToken, "GET", "/api/users/"+user.ID, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET own user: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doBearerRequest(t, env.srv, userToken, "GET", "/api/users/"+env.adminUser.ID, nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("GET other user: status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}

	adminPaths := []struct {
		method string
		path   string
	}{
		{"GET", "/api/users"},
		{"PATCH", "/api/users/" + env.adminUser.ID + "/role"},
		{"PATCH", "/api/users/" + env.adminUser.ID + "/active"},
		{"GET", "/api/users/" + env.adminUser.ID + "/agents"},
		{"PATCH", "/api/users/" + env.adminUser.ID + "/agents"},
	}

	for _, tc := range adminPaths {
		rr := doBearerRequest(t, env.srv, userToken, tc.method, tc.path, nil)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s %s: status = %d, want %d (body: %s)",
				tc.method, tc.path, rr.Code, http.StatusForbidden, rr.Body.String())
		}
	}
}

func TestLegacyUserRoutesRemoved(t *testing.T) {
	env := setupAdmin(t)

	for _, path := range []string{"/api/auth/users", "/api/auth/profile/identities"} {
		rr := doRequest(t, env, "GET", path, nil)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status = %d, want %d (body: %s)", path, rr.Code, http.StatusNotFound, rr.Body.String())
		}
	}
}

func TestLegacyOAuthCallbackAliasRemainsPublic(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/api/auth/profile/oauth/feishu/callback?code=x&state=y", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("legacy callback alias: status = %d, want %d (body: %s)", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
}

func TestAdminSelfMemoryRejectsUnknownAgent(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PATCH", "/api/users/me/memories/not-an-agent", map[string]string{"content": "memory"})
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusNotFound, rr.Body.String())
	}
}

func TestNonAdminCanAccessOwnProfile(t *testing.T) {
	env := setupAdmin(t)

	_, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "normaluser", auth.RoleUser)

	rr := doBearerRequest(t, env.srv, userToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	var me struct {
		Role    string `json:"role"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(parseResponse(t, rr).Data, &me); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if me.Role != "user" {
		t.Errorf("role = %q, want %q", me.Role, "user")
	}
	if me.IsAdmin {
		t.Error("expected is_admin = false for regular user")
	}
}

func TestUserNotifyIdentityRequiresOwnedIdentity(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	user, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "notifyuser", auth.RoleUser)
	other, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "notifyother", auth.RoleUser)
	owned, err := env.oidcStore.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     user.ID,
		Platform:   "telegram",
		ExternalID: "tg-notify-user",
		Name:       "Notify User",
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity owned: %v", err)
	}
	foreign, err := env.oidcStore.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     other.ID,
		Platform:   "telegram",
		ExternalID: "tg-notify-other",
		Name:       "Notify Other",
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity foreign: %v", err)
	}

	rr := doBearerRequest(t, env.srv, userToken, "PATCH", "/api/users/me/notify-identity", map[string]any{"notify_identity_id": owned.ID})
	if rr.Code != http.StatusOK {
		t.Fatalf("set owned identity: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	rr = doBearerRequest(t, env.srv, userToken, "PATCH", "/api/users/me/notify-identity", map[string]any{"notify_identity_id": foreign.ID})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("set foreign identity: status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}

	rr = doBearerRequest(t, env.srv, userToken, "PATCH", "/api/users/me/notify-identity", map[string]any{"notify_identity_id": nil})
	if rr.Code != http.StatusOK {
		t.Fatalf("clear identity: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestRoleDemotionInvalidatesSessions(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Set up session-based auth.
	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	env.rebuild(t, func(d *server.Deps) {
		d.OIDC.AuthSvc = svc
		d.OIDC.SessionMgr = sessionMgr
	})

	// Create an admin user via OIDC flow.
	result, err := svc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "test",
		Subject:  "sub-admin-demote",
		Email:    "demote@test.local",
		Name:     "Admin Demote",
	}, sessionMgr)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}
	if err := env.oidcStore.UpdateUserRole(ctx, result.User.ID, auth.RoleAdmin); err != nil {
		t.Fatalf("promote to admin: %v", err)
	}

	// Session should work.
	rr := doSessionRequest(t, env.srv, result.SessionToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("before demotion: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Demote via the admin API (using the original admin's bearer token).
	body := map[string]string{"role": "user"}
	rr = doRequest(t, env, "PATCH", "/api/users/"+result.User.ID+"/role", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("demote: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// The demoted user's session should be invalidated.
	rr = doSessionRequest(t, env.srv, result.SessionToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("after demotion: status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func TestDeactivationInvalidatesSessions(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	svc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	env.rebuild(t, func(d *server.Deps) {
		d.OIDC.AuthSvc = svc
		d.OIDC.SessionMgr = sessionMgr
	})

	result, err := svc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "test",
		Subject:  "sub-deactivate-session",
		Email:    "deactivatesession@test.local",
		Name:     "Deactivate Session",
	}, sessionMgr)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}

	// Session works.
	rr := doSessionRequest(t, env.srv, result.SessionToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("before deactivation: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Deactivate via admin API.
	rr = doRequest(t, env, "PATCH", "/api/users/"+result.User.ID+"/active", map[string]any{"is_active": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("deactivate: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Session should be invalidated.
	rr = doSessionRequest(t, env.srv, result.SessionToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("after deactivation: status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestSeedDataPresent(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	agents, err := env.store.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %v, want one Stella agent", agents)
	}
	if got := agents[0]; got.ID != "stella" || got.Name != "Stella" || !got.Enabled || got.Model != "" {
		t.Errorf("seeded agent = %+v, want enabled Stella with id stella and empty model", got)
	}

	providers, err := env.store.ListProviders(ctx)
	if err != nil {
		t.Fatalf("ListProviders: %v", err)
	}
	if len(providers) != 0 {
		t.Errorf("providers = %v, want none", providers)
	}
	channels, err := env.store.ListChannels(ctx)
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 0 {
		t.Errorf("channels = %v, want none", channels)
	}
}

func TestSeedDataAccessibleViaAPI(t *testing.T) {
	env := setupAdmin(t)

	// Agents via API.
	rr := doRequest(t, env, "GET", "/api/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/agents: status = %d", rr.Code)
	}
	var agents []config.Agent
	if err := json.Unmarshal(parseListItems(t, rr, "agents"), &agents); err != nil {
		t.Fatalf("unmarshal agents: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("no agents returned from API")
	}

	// Providers via API.
	rr = doRequest(t, env, "GET", "/api/providers", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/providers: status = %d", rr.Code)
	}
	var providers []config.Provider
	if err := json.Unmarshal(parseListItems(t, rr, "providers"), &providers); err != nil {
		t.Fatalf("unmarshal providers: %v", err)
	}
	if len(providers) != 0 {
		t.Fatalf("providers returned from API = %v, want none", providers)
	}
}

func TestFullUserLifecycle(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// 1. Create a regular user.
	user, userToken := createTestUserWithToken(t, env.authStore, env.oidcStore, "lifecycle", auth.RoleUser)

	// 2. User can access their own profile.
	rr := doBearerRequest(t, env.srv, userToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("step 2: status = %d, want %d", rr.Code, http.StatusOK)
	}

	// 3. User cannot access admin endpoints.
	rr = doBearerRequest(t, env.srv, userToken, "GET", "/api/users", nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("step 3: status = %d, want %d", rr.Code, http.StatusForbidden)
	}

	// 4. Admin promotes user to admin.
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/role", map[string]string{"role": "admin"})
	if rr.Code != http.StatusOK {
		t.Fatalf("step 4 promote: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// 5. Role changes invalidate sessions. Verify the user record now has admin
	// role, then create a fresh session for the promoted user.
	updated, err := env.oidcStore.GetUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("step 5: GetUser: %v", err)
	}
	if updated.Role != auth.RoleAdmin {
		t.Fatalf("step 5: role = %q, want %q", updated.Role, auth.RoleAdmin)
	}
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key")
	if err != nil {
		t.Fatalf("step 5: NewSessionManager: %v", err)
	}
	userToken, _, err = sessionMgr.Create(ctx, user.ID)
	if err != nil {
		t.Fatalf("step 5: CreateSession: %v", err)
	}

	// 6. Admin can now access admin endpoints with their session.
	rr = doBearerRequest(t, env.srv, userToken, "GET", "/api/users", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("step 6: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// 7. Demote back to user.
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/role", map[string]string{"role": "user"})
	if rr.Code != http.StatusOK {
		t.Fatalf("step 7 demote: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// 8. Demotion invalidates the promoted session.
	rr = doBearerRequest(t, env.srv, userToken, "GET", "/api/users", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("step 8: status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	// 9. Deactivate user.
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/active", map[string]any{"is_active": false})
	if rr.Code != http.StatusOK {
		t.Fatalf("step 9 deactivate: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// 10. Bearer auth completely blocked.
	rr = doBearerRequest(t, env.srv, userToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("step 10: status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}

	// 11. Reactivate user.
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/active", map[string]any{"is_active": true})
	if rr.Code != http.StatusOK {
		t.Fatalf("step 11 reactivate: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// 12. A fresh session works again after reactivation.
	userToken, _, err = sessionMgr.Create(ctx, user.ID)
	if err != nil {
		t.Fatalf("step 12: CreateSession: %v", err)
	}
	rr = doBearerRequest(t, env.srv, userToken, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("step 12: status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestAgentAssignmentLifecycle(t *testing.T) {
	env := setupAdmin(t)

	user, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "agentlifecycle", auth.RoleUser)
	stellaID := findStellaID(t, env)

	// Initially no agents assigned.
	rr := doRequest(t, env, "GET", "/api/users/"+user.ID+"/agents", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list: status = %d", rr.Code)
	}
	var ids []string
	_ = json.Unmarshal(parseListItems(t, rr, "agent_ids"), &ids)
	if len(ids) != 0 {
		t.Fatalf("expected 0 agents, got %d", len(ids))
	}

	// Assign Stella.
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/agents", map[string]any{"agent_ids": []string{stellaID}})
	if rr.Code != http.StatusOK {
		t.Fatalf("assign: status = %d", rr.Code)
	}

	// Verify.
	rr = doRequest(t, env, "GET", "/api/users/"+user.ID+"/agents", nil)
	_ = json.Unmarshal(parseListItems(t, rr, "agent_ids"), &ids)
	if len(ids) != 1 || ids[0] != stellaID {
		t.Fatalf("expected [%s], got %v", stellaID, ids)
	}

	// Create a second agent and assign both.
	secondAgent := createTestAgent(t, env, config.Agent{
		Name:    "TestBot",
		Enabled: true,
		Scope:   "system",
	})
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/agents", map[string]any{"agent_ids": []string{stellaID, secondAgent}})
	if rr.Code != http.StatusOK {
		t.Fatalf("assign both: status = %d", rr.Code)
	}
	rr = doRequest(t, env, "GET", "/api/users/"+user.ID+"/agents", nil)
	_ = json.Unmarshal(parseListItems(t, rr, "agent_ids"), &ids)
	if len(ids) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(ids))
	}

	// Remove all.
	rr = doRequest(t, env, "PATCH", "/api/users/"+user.ID+"/agents", map[string]any{"agent_ids": []string{}})
	if rr.Code != http.StatusOK {
		t.Fatalf("remove all: status = %d", rr.Code)
	}
	rr = doRequest(t, env, "GET", "/api/users/"+user.ID+"/agents", nil)
	ids = nil
	_ = json.Unmarshal(parseListItems(t, rr, "agent_ids"), &ids)
	if len(ids) != 0 {
		t.Fatalf("expected 0 agents after removal, got %d", len(ids))
	}
}

func TestIdentityManagementLifecycle(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	user, _ := createTestUserWithToken(t, env.authStore, env.oidcStore, "identuser", auth.RoleUser)

	// Link a login identity.
	body := map[string]string{
		"provider":         "github",
		"provider_subject": "gh-12345",
		"email":            "identuser@github.com",
		"name":             "identuser",
	}
	rr := doRequest(t, env, "POST", "/api/users/"+user.ID+"/identities/login", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("link login identity: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// List login identities.
	rr = doRequest(t, env, "GET", "/api/users/"+user.ID+"/identities/login", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list login identities: status = %d", rr.Code)
	}
	var loginIdents []auth.LoginIdentity
	_ = json.Unmarshal(parseListItems(t, rr, "identities"), &loginIdents)
	if len(loginIdents) != 1 || loginIdents[0].Provider != "github" {
		t.Fatalf("unexpected login identities: %v", loginIdents)
	}

	// Link a channel identity directly in DB.
	chanIdent, err := env.oidcStore.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     user.ID,
		Platform:   "telegram",
		ExternalID: "tg-99999",
		Name:       "TG identuser",
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity: %v", err)
	}

	// List channel identities.
	rr = doRequest(t, env, "GET", "/api/users/"+user.ID+"/identities/channel", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list channel identities: status = %d", rr.Code)
	}
	var chanIdents []auth.ChannelIdentity
	_ = json.Unmarshal(parseListItems(t, rr, "identities"), &chanIdents)
	if len(chanIdents) != 1 || chanIdents[0].Platform != "telegram" {
		t.Fatalf("unexpected channel identities: %v", chanIdents)
	}

	// Delete channel identity.
	rr = doRequest(t, env, "DELETE", "/api/users/"+user.ID+"/identities/"+chanIdent.ID, nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete identity: status = %d (body: %s)", rr.Code, rr.Body.String())
	}

	// Verify deleted.
	rr = doRequest(t, env, "GET", "/api/users/"+user.ID+"/identities/channel", nil)
	chanIdents = nil
	_ = json.Unmarshal(parseListItems(t, rr, "identities"), &chanIdents)
	if len(chanIdents) != 0 {
		t.Fatalf("expected 0 channel identities after deletion, got %d", len(chanIdents))
	}
}

// --- helpers for session-based auth tests ---

// doSessionRequest makes a request with a session cookie (not a bearer token).
func doSessionRequest(t *testing.T, srv *server.Server, sessionToken, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: sessionToken})
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}
