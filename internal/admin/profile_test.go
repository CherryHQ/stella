package admin_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/vaayne/anna/internal/auth"
)

func TestListProfileIdentitiesEmpty(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/profile/identities", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var identities []auth.Identity
	if err := json.Unmarshal(resp.Data, &identities); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(identities) != 0 {
		t.Errorf("expected 0 identities, got %d", len(identities))
	}
}

func TestListProfileIdentitiesWithLink(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create an identity for the admin user.
	_, err := env.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     env.adminUser.ID,
		Platform:   "telegram",
		ExternalID: "12345",
		Name:       "TestAdmin",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	rr := doRequest(t, env, "GET", "/api/auth/profile/identities", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}

	resp := parseResponse(t, rr)
	var identities []auth.Identity
	if err := json.Unmarshal(resp.Data, &identities); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(identities) != 1 {
		t.Fatalf("expected 1 identity, got %d", len(identities))
	}
	if identities[0].Platform != "telegram" {
		t.Errorf("platform = %q, want %q", identities[0].Platform, "telegram")
	}
}

func TestChangePasswordSuccess(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"current_password": "testpassword",
		"new_password":     "newpassword123",
	}
	rr := doRequest(t, env, "PUT", "/api/auth/profile/password", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify the new password works.
	user, err := env.authStore.GetUser(context.Background(), env.adminUser.ID)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if err := auth.CheckPassword(user.PasswordHash, "newpassword123"); err != nil {
		t.Error("new password should work after change")
	}
}

func TestChangePasswordWrongCurrent(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"current_password": "wrongpassword",
		"new_password":     "newpassword123",
	}
	rr := doRequest(t, env, "PUT", "/api/auth/profile/password", body)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestChangePasswordTooShort(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"current_password": "testpassword",
		"new_password":     "short",
	}
	rr := doRequest(t, env, "PUT", "/api/auth/profile/password", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestGenerateLinkCode(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"platform": "telegram",
	}
	rr := doRequest(t, env, "POST", "/api/auth/profile/link-code", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	resp := parseResponse(t, rr)
	var result struct {
		Code     string `json:"code"`
		Platform string `json:"platform"`
	}
	if err := json.Unmarshal(resp.Data, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(result.Code) != 6 {
		t.Errorf("code length = %d, want 6", len(result.Code))
	}
	if result.Platform != "telegram" {
		t.Errorf("platform = %q, want %q", result.Platform, "telegram")
	}
}

func TestGenerateLinkCodeInvalidPlatform(t *testing.T) {
	env := setupAdmin(t)

	body := map[string]string{
		"platform": "invalid",
	}
	rr := doRequest(t, env, "POST", "/api/auth/profile/link-code", body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestUnlinkIdentity(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create an identity.
	identity, err := env.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     env.adminUser.ID,
		Platform:   "telegram",
		ExternalID: "54321",
		Name:       "TestAdmin",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	rr := doRequest(t, env, "DELETE", "/api/auth/profile/identities/"+itoa(identity.ID), nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}

	// Verify it's gone.
	identities, err := env.authStore.ListIdentitiesByUser(ctx, env.adminUser.ID)
	if err != nil {
		t.Fatalf("ListIdentitiesByUser: %v", err)
	}
	if len(identities) != 0 {
		t.Errorf("expected 0 identities after unlink, got %d", len(identities))
	}
}

func TestUnlinkIdentityOtherUser(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Create another user.
	hash, _ := auth.HashPassword("otherpassword")
	otherUser, err := env.authStore.CreateUser(ctx, "otheruser", hash)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Create an identity for the other user.
	identity, err := env.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     otherUser.ID,
		Platform:   "qq",
		ExternalID: "99999",
		Name:       "Other",
	})
	if err != nil {
		t.Fatalf("CreateIdentity: %v", err)
	}

	// Try to unlink it as the admin — should fail (not your identity).
	rr := doRequest(t, env, "DELETE", "/api/auth/profile/identities/"+itoa(identity.ID), nil)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

func TestProfilePageRoute(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/profile", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/html; charset=utf-8")
	}
}

func itoa(i int64) string {
	return strconv.FormatInt(i, 10)
}
