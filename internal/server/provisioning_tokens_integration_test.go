package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

type provisioningTokenResponse struct {
	Token             string `json:"token"`
	ProvisioningToken struct {
		ID        string     `json:"id"`
		ExpiresAt *time.Time `json:"expires_at"`
	} `json:"provisioning_token"`
}

func createProvisioningToken(t *testing.T, srvToken string, env *testEnv, name string, body map[string]any) provisioningTokenResponse {
	t.Helper()
	if body == nil {
		body = map[string]any{"name": name}
	}
	rr := doRequestWithSession(t, env.srv, srvToken, http.MethodPost, "/api/admin/provisioning-tokens", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create provisioning token: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out provisioningTokenResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode provisioning token: %v", err)
	}
	if out.Token == "" || !strings.HasPrefix(out.Token, "stella_prv_") || out.ProvisioningToken.ID == "" {
		t.Fatalf("create response must contain one-time provisioning token and metadata: %s", rr.Body.String())
	}
	return out
}

// TestProvisioningTokenLifecycle proves the complete interactive-admin lifecycle
// while retaining strict metadata and personal/provisioning isolation.
func TestProvisioningTokenLifecycle(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	created := createProvisioningToken(t, env.bearerToken, env, "directory-sync", nil)

	if created.ProvisioningToken.ExpiresAt == nil {
		t.Fatal("provisioning tokens must default to an expiry")
	}
	if d := time.Until(*created.ProvisioningToken.ExpiresAt); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("default expiry want ~90d, got %v", d)
	}
	var tokenUse, tokenHash string
	var expiresAt *time.Time
	if err := env.db.QueryRow(ctx, "select token_use, token_hash, expires_at from personal_access_token where id=$1", created.ProvisioningToken.ID).Scan(&tokenUse, &tokenHash, &expiresAt); err != nil {
		t.Fatalf("query provisioning token: %v", err)
	}
	if tokenUse != "provisioning" || tokenHash == created.Token || expiresAt == nil {
		t.Fatalf("stored provisioning token use=%q hash plaintext=%v expiry=%v", tokenUse, tokenHash == created.Token, expiresAt)
	}

	// The resource list exposes metadata only, never plaintext, hashes, or the
	// legacy scope column. A normal personal token stays out of this list.
	personal, personalID := mintPAT(t, env, env.bearerToken, "personal-isolation")
	if personal == "" || personalID == "" {
		t.Fatal("personal token fixture missing")
	}
	list := doRequest(t, env, http.MethodGet, "/api/admin/provisioning-tokens", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("list provisioning tokens: %d %s", list.Code, list.Body.String())
	}
	if strings.Contains(list.Body.String(), created.Token) || strings.Contains(list.Body.String(), "token_hash") || strings.Contains(list.Body.String(), "scopes") || strings.Contains(list.Body.String(), personalID) {
		t.Fatalf("provisioning list leaked secret/internal data or personal token: %s", list.Body.String())
	}
	personalList := doRequest(t, env, http.MethodGet, "/api/users/me/tokens", nil)
	if personalList.Code != http.StatusOK || !strings.Contains(personalList.Body.String(), personalID) || strings.Contains(personalList.Body.String(), created.ProvisioningToken.ID) {
		t.Fatalf("personal list must isolate provisioning token: %d %s", personalList.Code, personalList.Body.String())
	}
	if rr := doRequest(t, env, http.MethodGet, "/api/users/me/tokens/"+created.ProvisioningToken.ID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("personal get must not see provisioning token: want 404 got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/users/me/tokens/"+created.ProvisioningToken.ID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("personal revoke must not manage provisioning token: want 404 got %d (%s)", rr.Code, rr.Body.String())
	}

	// The public contract intentionally has no never_expires field; an obsolete
	// JSON member cannot manufacture an unbounded provisioning credential.
	legacy := createProvisioningToken(t, env.bearerToken, env, "legacy", map[string]any{"name": "legacy", "never_expires": true})
	if legacy.ProvisioningToken.ExpiresAt == nil {
		t.Fatal("unknown never_expires field must not remove provisioning expiry")
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/admin/provisioning-tokens", map[string]any{"name": "third"}); rr.Code != http.StatusConflict {
		t.Fatalf("third active provisioning token: want 409 got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/admin/provisioning-tokens/"+legacy.ProvisioningToken.ID, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke rotation-overlap token: want 204 got %d (%s)", rr.Code, rr.Body.String())
	}
	for _, body := range []map[string]any{
		{"name": "", "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339)},
		{"name": "past", "expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339)},
		{"name": "long", "expires_at": time.Now().Add(366 * 24 * time.Hour).Format(time.RFC3339)},
	} {
		if rr := doRequest(t, env, http.MethodPost, "/api/admin/provisioning-tokens", body); rr.Code != http.StatusBadRequest {
			t.Fatalf("invalid provisioning create %#v: want 400 got %d (%s)", body, rr.Code, rr.Body.String())
		}
	}
	expired := createProvisioningToken(t, env.bearerToken, env, "expired", nil)
	if _, err := env.db.Exec(ctx, "update personal_access_token set expires_at = now() - interval '1 hour' where id=$1", expired.ProvisioningToken.ID); err != nil {
		t.Fatalf("force-expire provisioning token: %v", err)
	}
	if rr := doBearerRequest(t, env.srv, expired.Token, http.MethodGet, "/api/provisioned-users", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expired provisioning bearer: want 401 got %d (%s)", rr.Code, rr.Body.String())
	}

	if rr := doRequest(t, env, http.MethodDelete, "/api/admin/provisioning-tokens/"+created.ProvisioningToken.ID, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke provisioning token: want 204 got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doBearerRequest(t, env.srv, created.Token, http.MethodGet, "/api/provisioned-users", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("revoked provisioning bearer: want 401 got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestProvisioningTokenSessionAndOwnerGuards proves no bearer can mint, list,
// or revoke this credential family, and owner role/active state applies at use.
func TestProvisioningTokenSessionAndOwnerGuards(t *testing.T) {
	env := setupAdmin(t)
	created := createProvisioningToken(t, env.bearerToken, env, "guard", nil)
	adminPAT, _ := mintPAT(t, env, env.bearerToken, "admin-bearer")
	clientID, secret := registerOAuthClientAPI(t, env, "https://provision.example/cb", []string{"agent:read"})
	code := authorizeOAuthClient(t, env.srv, env.bearerToken, clientID, "https://provision.example/cb", "agent:read")
	oauthToken := exchangeOAuthToken(t, env.srv, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code":          {code},
		"redirect_uri":  {"https://provision.example/cb"},
	}).AccessToken
	for _, bearer := range []string{adminPAT, oauthToken, created.Token} {
		for _, tc := range []struct{ method, path string }{
			{http.MethodGet, "/api/admin/provisioning-tokens"},
			{http.MethodPost, "/api/admin/provisioning-tokens"},
			{http.MethodDelete, "/api/admin/provisioning-tokens/" + created.ProvisioningToken.ID},
		} {
			if rr := doBearerRequest(t, env.srv, bearer, tc.method, tc.path, map[string]any{"name": "self-mint"}); rr.Code != http.StatusForbidden {
				t.Fatalf("bearer %s %s: want 403 got %d (%s)", tc.method, tc.path, rr.Code, rr.Body.String())
			}
		}
	}
	_, userSession := createTestUserWithToken(t, env.authStore, env.oidcStore, "provision-non-admin", auth.RoleUser)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/admin/provisioning-tokens"},
		{http.MethodPost, "/api/admin/provisioning-tokens"},
		{http.MethodDelete, "/api/admin/provisioning-tokens/" + created.ProvisioningToken.ID},
	} {
		if rr := doRequestWithSession(t, env.srv, userSession, tc.method, tc.path, map[string]any{"name": "nope"}); rr.Code != http.StatusForbidden {
			t.Fatalf("non-admin session %s %s: want 403 got %d (%s)", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}

	owner, ownerSession := createTestUserWithToken(t, env.authStore, env.oidcStore, "provision-owner", auth.RoleAdmin)
	owned := createProvisioningToken(t, ownerSession, env, "owned", nil)
	if rr := doRequestWithSession(t, env.srv, ownerSession, http.MethodGet, "/api/admin/provisioning-tokens", nil); rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), created.ProvisioningToken.ID) || !strings.Contains(rr.Body.String(), owned.ProvisioningToken.ID) {
		t.Fatalf("owner provisioning list must be isolated: %d %s", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPatch, "/api/users/"+owner.ID+"/role", map[string]any{"role": auth.RoleUser}); rr.Code != http.StatusOK {
		t.Fatalf("demote provisioning owner: %d %s", rr.Code, rr.Body.String())
	}
	if rr := doBearerRequest(t, env.srv, owned.Token, http.MethodGet, "/api/provisioned-users", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("demoted owner token: want 401 got %d (%s)", rr.Code, rr.Body.String())
	}

	owner2, owner2Session := createTestUserWithToken(t, env.authStore, env.oidcStore, "provision-deactivate", auth.RoleAdmin)
	deactivated := createProvisioningToken(t, owner2Session, env, "deactivated", nil)
	if rr := doRequest(t, env, http.MethodPatch, "/api/users/"+owner2.ID+"/active", map[string]any{"is_active": false}); rr.Code != http.StatusOK {
		t.Fatalf("deactivate provisioning owner: %d %s", rr.Code, rr.Body.String())
	}
	var revokedAt *time.Time
	if err := env.db.QueryRow(context.Background(), "select revoked_at from personal_access_token where id=$1", deactivated.ProvisioningToken.ID).Scan(&revokedAt); err != nil || revokedAt == nil {
		t.Fatalf("deactivation must revoke provisioning token: revoked_at=%v err=%v", revokedAt, err)
	}
	if rr := doBearerRequest(t, env.srv, deactivated.Token, http.MethodGet, "/api/provisioned-users", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("deactivated owner token: want 401 got %d (%s)", rr.Code, rr.Body.String())
	}
}
