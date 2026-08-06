package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/credential"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type provisionedUserResponse struct {
	ProvisionedUser struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		ExternalID  string `json:"external_id"`
		Email       string `json:"email"`
		Role        string `json:"role"`
		IsActive    bool   `json:"is_active"`
		ActiveToken *struct {
			ID string `json:"id"`
		} `json:"active_token"`
	} `json:"provisioned_user"`
	Token string `json:"token"`
}

func createProvisionedUserHTTP(t *testing.T, env *testEnv, bearer string, body map[string]any) provisionedUserResponse {
	t.Helper()
	rr := doBearerRequest(t, env.srv, bearer, http.MethodPost, "/api/provisioned-users", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create provisioned user: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out provisionedUserResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode provisioned user: %v", err)
	}
	if out.Token == "" || !strings.HasPrefix(out.Token, "stella_pat_") || out.ProvisionedUser.ID == "" {
		t.Fatalf("invalid provisioned-user create response: %s", rr.Body.String())
	}
	return out
}

// TestProvisionedUsersHTTPIntegration exercises the real credential middleware,
// handlers, provisioning service, account lifecycle, and PostgreSQL state.
func TestProvisionedUsersHTTPIntegration(t *testing.T) {
	ctx := context.Background()
	env := setupAdmin(t)
	issuer := createProvisioningToken(t, env.bearerToken, env, "directory", nil)

	// Only the dedicated provisioning bearer enters this resource family.
	adminPAT, _ := mintPAT(t, env, env.bearerToken, "admin-pat")
	clientID, secret := registerOAuthClientAPI(t, env, "https://provision.example/cb", []string{"agent:read"})
	code := authorizeOAuthClient(t, env.srv, env.bearerToken, clientID, "https://provision.example/cb", "agent:read")
	oauthToken := exchangeOAuthToken(t, env.srv, url.Values{
		"grant_type": {"authorization_code"}, "client_id": {clientID}, "client_secret": {secret}, "code": {code}, "redirect_uri": {"https://provision.example/cb"},
	}).AccessToken
	for _, denied := range []string{env.bearerToken, adminPAT, oauthToken} {
		if rr := doBearerRequest(t, env.srv, denied, http.MethodGet, "/api/provisioned-users", nil); rr.Code != http.StatusForbidden {
			t.Fatalf("non-provisioning credential: want 403 got %d (%s)", rr.Code, rr.Body.String())
		}
	}

	created := createProvisionedUserHTTP(t, env, issuer.Token, map[string]any{
		"external_id": "directory-ada", "email": " Ada@Example.Test ", "name": "Ada",
	})
	if created.ProvisionedUser.Email != "ada@example.test" || created.ProvisionedUser.Role != auth.RoleUser || !created.ProvisionedUser.IsActive {
		t.Fatalf("created user metadata = %#v", created.ProvisionedUser)
	}
	var role string
	var active bool
	var passwordCount, loginIdentityCount, channelIdentityCount, sessionCount int
	if err := env.db.QueryRow(ctx, `SELECT user_id FROM auth_provisioned_user WHERE id=$1`, created.ProvisionedUser.ID).Scan(&created.ProvisionedUser.UserID); err != nil {
		t.Fatalf("load provisioned user mapping: %v", err)
	}
	if err := env.db.QueryRow(ctx, `SELECT role, is_active FROM auth_user WHERE id=$1`, created.ProvisionedUser.UserID).Scan(&role, &active); err != nil {
		t.Fatalf("load created auth user: %v", err)
	}
	for _, check := range []struct {
		query string
		out   *int
	}{
		{`SELECT count(*) FROM auth_credential WHERE user_id=$1`, &passwordCount},
		{`SELECT count(*) FROM auth_identity WHERE user_id=$1`, &loginIdentityCount},
		{`SELECT count(*) FROM channel_identity WHERE user_id=$1`, &channelIdentityCount},
		{`SELECT count(*) FROM auth_session WHERE user_id=$1`, &sessionCount},
	} {
		if err := env.db.QueryRow(ctx, check.query, created.ProvisionedUser.UserID).Scan(check.out); err != nil {
			t.Fatalf("creation invariant query: %v", err)
		}
	}
	if role != auth.RoleUser || !active || passwordCount != 0 || loginIdentityCount != 0 || channelIdentityCount != 0 || sessionCount != 0 {
		t.Fatalf("creation invariants role=%q active=%v password=%d login=%d channel=%d session=%d", role, active, passwordCount, loginIdentityCount, channelIdentityCount, sessionCount)
	}
	var tokenHash string
	var expiresAt *time.Time
	var issuedBy string
	var issuedByProvisioning bool
	if err := env.db.QueryRow(ctx, `SELECT token_hash, expires_at, issued_by_token_id, issued_by_provisioning FROM personal_access_token WHERE user_id=$1 AND issued_by_provisioning`, created.ProvisionedUser.UserID).Scan(&tokenHash, &expiresAt, &issuedBy, &issuedByProvisioning); err != nil {
		t.Fatalf("load provisioning PAT: %v", err)
	}
	if tokenHash == created.Token || expiresAt == nil || time.Until(*expiresAt) < 89*24*time.Hour || time.Until(*expiresAt) > 91*24*time.Hour || issuedBy != issuer.ProvisioningToken.ID || !issuedByProvisioning {
		t.Fatalf("provisioning PAT invariant hashPlaintext=%v expires=%v issuer=%q marker=%v", tokenHash == created.Token, expiresAt, issuedBy, issuedByProvisioning)
	}
	if rr := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users", map[string]any{"external_id": "directory-too-long", "email": "too-long@example.test", "name": "Too long", "expires_at": time.Now().UTC().Add(366 * 24 * time.Hour).Format(time.RFC3339)}); rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong create expiry: want 400 got %d (%s)", rr.Code, rr.Body.String())
	}

	// GET/list reveal safe metadata only. A duplicate directory retry returns the
	// existing safe resource but cannot replay its one-time PAT.
	for _, path := range []string{"/api/provisioned-users/" + created.ProvisionedUser.ID, "/api/provisioned-users?page_size=1"} {
		rr := doBearerRequest(t, env.srv, issuer.Token, http.MethodGet, path, nil)
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), created.Token) || strings.Contains(rr.Body.String(), tokenHash) || strings.Contains(rr.Body.String(), "scopes") || strings.Contains(rr.Body.String(), "issued_by_token_id") {
			t.Fatalf("safe metadata %s: status=%d body=%s", path, rr.Code, rr.Body.String())
		}
	}
	duplicate := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users", map[string]any{"external_id": "directory-ada", "email": "other@example.test", "name": "Other"})
	if duplicate.Code != http.StatusConflict || strings.Contains(duplicate.Body.String(), created.Token) || strings.Contains(duplicate.Body.String(), tokenHash) {
		t.Fatalf("duplicate external id: status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}
	var managedCount, managedPATCount int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM auth_provisioned_user WHERE external_id='directory-ada'`).Scan(&managedCount); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id=$1 AND issued_by_provisioning`, created.ProvisionedUser.UserID).Scan(&managedPATCount); err != nil {
		t.Fatal(err)
	}
	if managedCount != 1 || managedPATCount != 1 {
		t.Fatalf("duplicate created records users=%d pats=%d", managedCount, managedPATCount)
	}

	// Ordinary-registration normalization is reused: case/whitespace cannot make
	// an unmanaged email collision observable or bypass uniqueness.
	if _, err := env.oidcStore.CreateUser(ctx, auth.User{ID: uuid.NewString(), Email: "unmanaged@example.test", Name: "Unmanaged", Role: auth.RoleUser, IsActive: true}); err != nil {
		t.Fatalf("create unmanaged fixture: %v", err)
	}
	unmanaged := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users", map[string]any{"external_id": "directory-unmanaged", "email": " UnManaged@Example.Test ", "name": "Collision"})
	if unmanaged.Code != http.StatusConflict || strings.Contains(unmanaged.Body.String(), "provisioned_user") || strings.Contains(unmanaged.Body.String(), "unmanaged@example.test") {
		t.Fatalf("unmanaged email collision: status=%d body=%s", unmanaged.Code, unmanaged.Body.String())
	}

	// An expired unrevoked token is not reported as active. Rotation nevertheless
	// revokes it and every other non-revoked provisioning-issued token before the
	// single replacement is created.
	if _, err := env.db.Exec(ctx, `UPDATE personal_access_token SET expires_at = now() - interval '1 hour' WHERE user_id=$1 AND issued_by_provisioning`, created.ProvisionedUser.UserID); err != nil {
		t.Fatalf("expire provisioning PAT: %v", err)
	}
	expiredGet := doBearerRequest(t, env.srv, issuer.Token, http.MethodGet, "/api/provisioned-users/"+created.ProvisionedUser.ID, nil)
	if expiredGet.Code != http.StatusOK || strings.Contains(expiredGet.Body.String(), `"active_token":{`) {
		t.Fatalf("expired token must not be active metadata: %d %s", expiredGet.Code, expiredGet.Body.String())
	}
	unrelated, err := credential.MintOpaque(credential.KindPAT)
	if err != nil {
		t.Fatalf("mint unrelated PAT: %v", err)
	}
	if _, err := sqlc.New(env.db).CreatePersonalAccessToken(ctx, sqlc.CreatePersonalAccessTokenParams{PublicID: unrelated.PublicID, UserID: created.ProvisionedUser.UserID, Name: "unrelated", TokenHash: unrelated.TokenHash, Last4: unrelated.Last4, Scopes: []string{}, TokenUse: "personal"}); err != nil {
		t.Fatalf("create unrelated PAT: %v", err)
	}
	rotated := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users/"+created.ProvisionedUser.ID+"/rotate-token", map[string]any{"token_name": "rotated", "expires_at": time.Now().UTC().Add(365 * 24 * time.Hour).Format(time.RFC3339)})
	if rotated.Code != http.StatusOK || strings.Contains(rotated.Body.String(), created.Token) {
		t.Fatalf("rotate: status=%d body=%s", rotated.Code, rotated.Body.String())
	}
	var activeProvisioned, activeUnrelated int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id=$1 AND issued_by_provisioning AND revoked_at IS NULL`, created.ProvisionedUser.UserID).Scan(&activeProvisioned); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id=$1 AND NOT issued_by_provisioning AND revoked_at IS NULL`, created.ProvisionedUser.UserID).Scan(&activeUnrelated); err != nil {
		t.Fatal(err)
	}
	if activeProvisioned != 1 || activeUnrelated != 1 {
		t.Fatalf("rotation PAT state provisioned=%d unrelated=%d", activeProvisioned, activeUnrelated)
	}
	if rr := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users/"+created.ProvisionedUser.ID+"/rotate-token", nil); rr.Code != http.StatusOK {
		t.Fatalf("repeat rotation: want 200 got %d (%s)", rr.Code, rr.Body.String())
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id=$1 AND issued_by_provisioning AND revoked_at IS NULL`, created.ProvisionedUser.UserID).Scan(&activeProvisioned); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id=$1 AND NOT issued_by_provisioning AND revoked_at IS NULL`, created.ProvisionedUser.UserID).Scan(&activeUnrelated); err != nil {
		t.Fatal(err)
	}
	if activeProvisioned != 1 || activeUnrelated != 1 {
		t.Fatalf("repeat rotation PAT state provisioned=%d unrelated=%d", activeProvisioned, activeUnrelated)
	}
	if rr := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users/"+created.ProvisionedUser.ID+"/rotate-token", map[string]any{"expires_at": time.Now().UTC().Add(366 * 24 * time.Hour).Format(time.RFC3339)}); rr.Code != http.StatusBadRequest {
		t.Fatalf("overlong rotation expiry: want 400 got %d (%s)", rr.Code, rr.Body.String())
	}

	// Deactivation goes through the real conditional account lifecycle and
	// revokes all sessions and both personal/provisioning token families.
	if _, err := env.oidcStore.CreateSession(ctx, auth.Session{ID: uuid.NewString(), UserID: created.ProvisionedUser.UserID, TokenHash: "deactivate-session", ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatalf("create provisioned session fixture: %v", err)
	}
	deactivated := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, "/api/provisioned-users/"+created.ProvisionedUser.ID+"/deactivate", nil)
	if deactivated.Code != http.StatusOK {
		t.Fatalf("deactivate: status=%d body=%s", deactivated.Code, deactivated.Body.String())
	}
	var stillActive, remainingSessions, remainingActivePATs int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM auth_user WHERE id=$1 AND is_active`, created.ProvisionedUser.UserID).Scan(&stillActive); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM auth_session WHERE user_id=$1`, created.ProvisionedUser.UserID).Scan(&remainingSessions); err != nil {
		t.Fatal(err)
	}
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM personal_access_token WHERE user_id=$1 AND revoked_at IS NULL`, created.ProvisionedUser.UserID).Scan(&remainingActivePATs); err != nil {
		t.Fatal(err)
	}
	if stillActive != 0 || remainingSessions != 0 || remainingActivePATs != 0 {
		t.Fatalf("deactivation not complete active=%d sessions=%d pats=%d", stillActive, remainingSessions, remainingActivePATs)
	}

	promoted := createProvisionedUserHTTP(t, env, issuer.Token, map[string]any{"external_id": "directory-admin", "email": "admin@example.test", "name": "Admin"})
	if err := env.db.QueryRow(ctx, `SELECT user_id FROM auth_provisioned_user WHERE id=$1`, promoted.ProvisionedUser.ID).Scan(&promoted.ProvisionedUser.UserID); err != nil {
		t.Fatalf("load promoted user mapping: %v", err)
	}
	if _, err := env.db.Exec(ctx, `UPDATE auth_user SET role='admin' WHERE id=$1`, promoted.ProvisionedUser.UserID); err != nil {
		t.Fatalf("promote provisioned user: %v", err)
	}
	for _, path := range []string{"/api/provisioned-users/" + promoted.ProvisionedUser.ID + "/rotate-token", "/api/provisioned-users/" + promoted.ProvisionedUser.ID + "/deactivate"} {
		if rr := doBearerRequest(t, env.srv, issuer.Token, http.MethodPost, path, nil); rr.Code != http.StatusForbidden {
			t.Fatalf("promoted target %s: want 403 got %d (%s)", path, rr.Code, rr.Body.String())
		}
	}
}
