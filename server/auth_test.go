package server_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestRegisterAndLogin(t *testing.T) {
	env := setupAdmin(t)

	// Register a new user.
	body := map[string]string{
		"username": "newuser",
		"password": "securepass123",
	}
	rr := doUnauthRequest(t, env.srv, "POST", "/api/auth/register", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Should have a session cookie.
	var sessionCookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("register should set session cookie")
	}

	// Login with the same credentials.
	body = map[string]string{
		"username": "newuser",
		"password": "securepass123",
	}
	rr = doUnauthRequest(t, env.srv, "POST", "/api/auth/login", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("login status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	sessionCookie = nil
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatal("login should set session cookie")
	}
}

func TestRegisterShortPassword(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"username": "shortpw",
		"password": "short",
	}
	rr := doUnauthRequest(t, env.srv, "POST", "/api/auth/register", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestRegisterDuplicateUsername(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"username": "dupuser",
		"password": "password123",
	}
	rr := doUnauthRequest(t, env.srv, "POST", "/api/auth/register", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("first register status = %d, want %d", rr.Code, http.StatusCreated)
	}

	rr = doUnauthRequest(t, env.srv, "POST", "/api/auth/register", body)
	if rr.Code != http.StatusConflict {
		t.Fatalf("duplicate register status = %d, want %d", rr.Code, http.StatusConflict)
	}
}

func TestLoginWrongPassword(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"username": "testadmin",
		"password": "wrongpassword",
	}
	rr := doUnauthRequest(t, env.srv, "POST", "/api/auth/login", body)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestLoginNonexistentUser(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"username": "noexist",
		"password": "anypassword",
	}
	rr := doUnauthRequest(t, env.srv, "POST", "/api/auth/login", body)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestLogout(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "POST", "/api/auth/logout", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	// Session cookie should be cleared.
	var cleared bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.SessionCookieName && c.MaxAge == -1 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout should clear session cookie")
	}
}

func TestMeEndpoint(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var me struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
		Role     string `json:"role"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(resp.Data, &me); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if me.Username != "testadmin" {
		t.Errorf("username = %q, want %q", me.Username, "testadmin")
	}
	if !me.IsAdmin {
		t.Error("expected is_admin = true")
	}
}

func TestMeUnauthenticated(t *testing.T) {
	env := setupAdmin(t)

	rr := doUnauthRequest(t, env.srv, "GET", "/api/auth/me", nil)
	// /api/auth/me is exempt from the auth middleware (it's under /api/auth/)
	// but the meHandler checks UserFromContext and returns 401.
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestFirstUserGetsAdminRole(t *testing.T) {
	env := setupAdmin(t)

	// The "testadmin" user already exists from setupAdmin. Create a fresh env
	// to test first-user logic: register a new user as the "second" user.
	body := map[string]string{
		"username": "seconduser",
		"password": "password123",
	}
	rr := doUnauthRequest(t, env.srv, "POST", "/api/auth/register", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register status = %d, want %d (body: %s)", rr.Code, http.StatusCreated, rr.Body.String())
	}

	// Check the second user's role: should be "user", not "admin".
	user, err := env.authStore.GetUserByUsername(context.Background(), "seconduser")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if user.Role == auth.RoleAdmin {
		t.Error("second user should not have admin role")
	}
	if user.Role != auth.RoleUser {
		t.Errorf("second user role = %q, want %q", user.Role, auth.RoleUser)
	}
}

func TestExpiredSessionDenied(t *testing.T) {
	env := setupAdmin(t)

	// Create a session that is already expired.
	expiredID := auth.NewSessionID()
	_, err := env.authStore.CreateSession(context.Background(), auth.Session{
		ID:        expiredID,
		UserID:    env.adminUser.ID,
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	rr := doRequestWithSession(t, env.srv, expiredID, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

type adminTokenVault struct {
	env map[int64]map[string]string
}

func newAdminTokenVault() *adminTokenVault {
	return &adminTokenVault{env: make(map[int64]map[string]string)}
}

func (v *adminTokenVault) Set(_ context.Context, userID int64, name string, plaintext string) error {
	if v.env[userID] == nil {
		v.env[userID] = make(map[string]string)
	}
	v.env[userID][name] = plaintext
	return nil
}

func (v *adminTokenVault) LoadEnv(_ context.Context, userID int64) (map[string]string, error) {
	return v.env[userID], nil
}

func TestBearerAuthSuccess(t *testing.T) {
	env := setupAdmin(t)
	vault := newAdminTokenVault()
	tokenSvc := auth.NewTokenService(env.authStore, vault)
	env.srv.SetTokenService(tokenSvc)

	if err := tokenSvc.EnsureAutoToken(context.Background(), env.adminUser.ID); err != nil {
		t.Fatalf("EnsureAutoToken: %v", err)
	}
	token := vault.env[env.adminUser.ID][auth.StellaTokenName]
	rr := doBearerRequest(t, env.srv, token, "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestBearerAuthRejectsExpiredToken(t *testing.T) {
	env := setupAdmin(t)
	env.srv.SetTokenService(auth.NewTokenService(env.authStore, newAdminTokenVault()))
	expired := time.Now().Add(-time.Hour)
	rawToken := "stella_expired"
	if _, err := env.authStore.CreateUserToken(context.Background(), auth.UserToken{
		UserID:      env.adminUser.ID,
		Name:        auth.StellaTokenName,
		TokenHash:   testTokenHash(rawToken),
		TokenPrefix: rawToken,
		ExpiresAt:   &expired,
	}); err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}

	rr := doBearerRequest(t, env.srv, rawToken, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuthRejectsRevokedToken(t *testing.T) {
	env := setupAdmin(t)
	env.srv.SetTokenService(auth.NewTokenService(env.authStore, newAdminTokenVault()))
	rawToken := "stella_revoked"
	token, err := env.authStore.CreateUserToken(context.Background(), auth.UserToken{
		UserID:      env.adminUser.ID,
		Name:        auth.StellaTokenName,
		TokenHash:   testTokenHash(rawToken),
		TokenPrefix: rawToken,
	})
	if err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}
	if _, err := env.authStore.RevokeUserToken(context.Background(), token.ID); err != nil {
		t.Fatalf("RevokeUserToken: %v", err)
	}

	rr := doBearerRequest(t, env.srv, rawToken, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuthRejectsInactiveUser(t *testing.T) {
	env := setupAdmin(t)
	vault := newAdminTokenVault()
	tokenSvc := auth.NewTokenService(env.authStore, vault)
	env.srv.SetTokenService(tokenSvc)
	if err := tokenSvc.EnsureAutoToken(context.Background(), env.adminUser.ID); err != nil {
		t.Fatalf("EnsureAutoToken: %v", err)
	}
	user := env.adminUser
	user.PasswordHash = "hash"
	user.IsActive = false
	if err := env.authStore.UpdateUser(context.Background(), user); err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}

	rr := doBearerRequest(t, env.srv, vault.env[env.adminUser.ID][auth.StellaTokenName], "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuthFallsBackToCookie(t *testing.T) {
	env := setupAdmin(t)
	env.srv.SetTokenService(auth.NewTokenService(env.authStore, newAdminTokenVault()))
	rr := doBearerRequestWithSession(t, env.srv, env.sessionID, "stella_wrong", "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func testTokenHash(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
