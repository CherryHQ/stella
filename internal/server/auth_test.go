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

func TestLogout(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "POST", "/api/auth/logout", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNoContent)
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
		ID      string `json:"id"`
		Role    string `json:"role"`
		IsAdmin bool   `json:"is_admin"`
	}
	if err := json.Unmarshal(resp.Data, &me); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if me.ID != env.adminUser.ID {
		t.Errorf("id = %q, want %q", me.ID, env.adminUser.ID)
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

func TestExpiredTokenDenied(t *testing.T) {
	env := setupAdmin(t)

	// Create a token that is already expired.
	expired := time.Now().Add(-time.Hour)
	rawToken := "stella_expired_session"
	if _, err := env.authStore.CreateUserToken(context.Background(), auth.UserToken{
		ID:          "expired-tok-id",
		UserID:      env.adminUser.ID,
		Name:        "test-expired",
		TokenHash:   testTokenHash(rawToken),
		TokenPrefix: rawToken[:15],
		ExpiresAt:   &expired,
	}); err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}

	rr := doBearerRequest(t, env.srv, rawToken, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuthSuccess(t *testing.T) {
	env := setupAdmin(t)
	tokenSvc := auth.NewTokenService(env.authStore)
	env.srv.SetTokenService(tokenSvc)
	token, err := tokenSvc.CreateScopedToken(context.Background(), env.adminUser.ID, "agent-1", "session-1", "")
	if err != nil {
		t.Fatalf("CreateScopedToken: %v", err)
	}

	rr := doBearerRequest(t, env.srv, token, "GET", "/api/status", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestBearerAuthRejectsLegacyToken(t *testing.T) {
	env := setupAdmin(t)
	env.srv.SetTokenService(auth.NewTokenService(env.authStore))
	rawToken := "stella_legacy"
	if _, err := env.authStore.CreateUserToken(context.Background(), auth.UserToken{
		UserID:      env.adminUser.ID,
		Name:        "STELLA_TOKEN",
		TokenHash:   testTokenHash(rawToken),
		TokenPrefix: rawToken,
	}); err != nil {
		t.Fatalf("CreateUserToken: %v", err)
	}

	rr := doBearerRequest(t, env.srv, rawToken, "GET", "/api/agents", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestBearerAuthRejectsExpiredToken(t *testing.T) {
	env := setupAdmin(t)
	env.srv.SetTokenService(auth.NewTokenService(env.authStore))
	expired := time.Now().Add(-time.Hour)
	rawToken := "stella_expired"
	if _, err := env.authStore.CreateUserToken(context.Background(), auth.UserToken{
		UserID:      env.adminUser.ID,
		Name:        "STELLA_TOKEN",
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
	env.srv.SetTokenService(auth.NewTokenService(env.authStore))
	rawToken := "stella_revoked"
	token, err := env.authStore.CreateUserToken(context.Background(), auth.UserToken{
		UserID:      env.adminUser.ID,
		Name:        "STELLA_TOKEN",
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

func TestBearerAuthWrongTokenDenied(t *testing.T) {
	env := setupAdmin(t)
	env.srv.SetTokenService(auth.NewTokenService(env.authStore))
	// A wrong bearer token with no valid fallback should be rejected.
	rr := doBearerRequest(t, env.srv, "stella_wrong_token", "GET", "/api/auth/me", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
}

func testTokenHash(rawToken string) string {
	sum := sha256.Sum256([]byte(rawToken))
	return hex.EncodeToString(sum[:])
}
