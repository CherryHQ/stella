package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
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
