package server_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"filippo.io/age"

	"github.com/CherryHQ/stella/internal/vault"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

func setupVaultEnv(t *testing.T) (*testEnv, *vault.Service) {
	t.Helper()
	env := setupAdmin(t)

	masterID, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	svc, err := vault.NewService(sqlc.New(env.db), masterID.String())
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	env.srv.SetVaultService(svc)

	// Provision age keys for the admin user.
	pubKey, encPrivKey, err := vault.GenerateUserKeys(svc.MasterRecipient())
	if err != nil {
		t.Fatalf("GenerateUserKeys: %v", err)
	}
	q := sqlc.New(env.db)
	if err := q.UpdateUserAgeKeys(t.Context(), sqlc.UpdateUserAgeKeysParams{
		AgePublicKey:  pubKey,
		AgePrivateKey: encPrivKey,
		ID:            env.adminUser.ID,
	}); err != nil {
		t.Fatalf("UpdateUserAgeKeys: %v", err)
	}

	return env, svc
}

func TestVaultNotConfigured(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "GET", "/api/auth/profile/vault", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestVaultCRUD(t *testing.T) {
	env, _ := setupVaultEnv(t)

	// List — empty.
	rr := doRequest(t, env, "GET", "/api/auth/profile/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d (body: %s)", rr.Code, http.StatusOK, rr.Body.String())
	}
	resp := parseResponse(t, rr)
	var entries []map[string]string
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	// Set a secret.
	rr = doRequest(t, env, "PUT", "/api/auth/profile/vault/GITHUB_TOKEN", map[string]string{"value": "ghp_test123"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("set status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	// List — one entry.
	rr = doRequest(t, env, "GET", "/api/auth/profile/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d", rr.Code, http.StatusOK)
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0]["name"] != "GITHUB_TOKEN" {
		t.Errorf("name = %q, want GITHUB_TOKEN", entries[0]["name"])
	}

	// Delete.
	rr = doRequest(t, env, "DELETE", "/api/auth/profile/vault/GITHUB_TOKEN", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want %d (body: %s)", rr.Code, http.StatusNoContent, rr.Body.String())
	}

	// List — empty again.
	rr = doRequest(t, env, "GET", "/api/auth/profile/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	resp = parseResponse(t, rr)
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries after delete, got %d", len(entries))
	}
}

func TestVaultSetValidationError(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/auth/profile/vault/invalid_name", map[string]string{"value": "test"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestVaultNotConfiguredPUT(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "PUT", "/api/auth/profile/vault/MY_KEY", map[string]string{"value": "v"})
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestVaultNotConfiguredDELETE(t *testing.T) {
	env := setupAdmin(t)

	rr := doRequest(t, env, "DELETE", "/api/auth/profile/vault/MY_KEY", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestVaultSetEmptyValue(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/auth/profile/vault/MY_KEY", map[string]string{"value": ""})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (body: %s)", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
}

func TestVaultSetEmptyName(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequest(t, env, "PUT", "/api/auth/profile/vault/", map[string]string{"value": "v"})
	// Empty name in path — either 400 or 404 depending on router
	if rr.Code == http.StatusNoContent {
		t.Fatal("expected error for empty name, got 204")
	}
}

func TestVaultSetInvalidJSON(t *testing.T) {
	env, _ := setupVaultEnv(t)

	rr := doRequestWithSession(t, env.srv, env.sessionID, "PUT", "/api/auth/profile/vault/MY_KEY", nil)
	// No body → JSON decode error or empty value
	if rr.Code == http.StatusNoContent {
		t.Fatal("expected error for nil body, got 204")
	}
}

func TestVaultUpdateExisting(t *testing.T) {
	env, _ := setupVaultEnv(t)

	// Set initial value.
	rr := doRequest(t, env, "PUT", "/api/auth/profile/vault/MY_SECRET", map[string]string{"value": "v1"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("set status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	// Update with new value.
	rr = doRequest(t, env, "PUT", "/api/auth/profile/vault/MY_SECRET", map[string]string{"value": "v2"})
	if rr.Code != http.StatusNoContent {
		t.Fatalf("update status = %d, want %d", rr.Code, http.StatusNoContent)
	}

	// List — still one entry.
	rr = doRequest(t, env, "GET", "/api/auth/profile/vault", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	resp := parseResponse(t, rr)
	var entries []map[string]string
	if err := json.Unmarshal(resp.Data, &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after update, got %d", len(entries))
	}
}
