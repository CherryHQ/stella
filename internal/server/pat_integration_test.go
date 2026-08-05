package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
)

// These integration tests drive the real server mux (middleware Resolve+Enforce
// -> generated handlers -> Postgres) for Personal Access Tokens. They cover the
// wired-up path the internal/credential unit tests cannot reach: a PAT minted
// through the create endpoint, presented as a Bearer against real routes, and
// the resulting rows in personal_access_token. The admin's legacy full-access
// bearer (env.bearerToken) creates the admin PATs that exercise the new boundary.

// mintPAT creates a PAT via POST /api/users/me/tokens using the given bearer and
// returns the one-time plaintext plus the token id.
func mintPAT(t *testing.T, env *testEnv, bearer, name string) (plaintext, id string) {
	t.Helper()
	rr := doBearerRequest(t, env.srv, bearer, http.MethodPost, "/api/users/me/tokens",
		map[string]any{"name": name})
	if rr.Code != http.StatusCreated {
		t.Fatalf("mintPAT(%s): status = %d, body = %s", name, rr.Code, rr.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
		PAT   struct {
			ID    string `json:"id"`
			Last4 string `json:"last4"`
		} `json:"personal_access_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("mintPAT(%s): decode: %v", name, err)
	}
	if resp.Token == "" || resp.PAT.ID == "" {
		t.Fatalf("mintPAT(%s): empty token/id: %s", name, rr.Body.String())
	}
	return resp.Token, resp.PAT.ID
}

// TestPATAuthority proves PATs enter every API route with the owner's current
// authority while the handler/domain boundary keeps non-admin users constrained.
func TestPATAuthority(t *testing.T) {
	env := setupAdmin(t)
	adminPAT, _ := mintPAT(t, env, env.bearerToken, "admin_control_plane")

	cases := []struct {
		name         string
		method, path string
		want         int
	}{
		{"account identity", http.MethodGet, "/api/auth/me", http.StatusOK},
		{"admin users", http.MethodGet, "/api/users", http.StatusOK},
		{"provider control plane", http.MethodGet, "/api/providers", http.StatusOK},
		{"model control plane", http.MethodGet, "/api/models", http.StatusOK},
		{"channel control plane", http.MethodGet, "/api/channels", http.StatusOK},
		{"plugin control plane", http.MethodGet, "/api/plugins", http.StatusOK},
		{"PAT management", http.MethodGet, "/api/users/me/tokens", http.StatusOK},
		{"non-api page", http.MethodGet, "/agents", http.StatusForbidden},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doBearerRequest(t, env.srv, adminPAT, c.method, c.path, nil)
			if rr.Code != c.want {
				t.Fatalf("%s %s: status = %d, want %d (body %s)", c.method, c.path, rr.Code, c.want, rr.Body.String())
			}
		})
	}

	_, userBearer := createTestUserWithToken(t, env.authStore, env.oidcStore, "pat_regular", auth.RoleUser)
	userPAT, _ := mintPAT(t, env, userBearer, "regular")
	if rr := doBearerRequest(t, env.srv, userPAT, http.MethodGet, "/api/users", nil); rr.Code != http.StatusForbidden {
		t.Fatalf("normal PAT GET /api/users: want 403, got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doBearerRequest(t, env.srv, userPAT, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusOK {
		t.Fatalf("normal PAT GET /api/agents: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	restrictedID := createTestAgent(t, env, config.Agent{
		Name:    "PAT ownership boundary",
		Model:   "anthropic/claude-sonnet-4-6",
		Scope:   config.AgentScopeRestricted,
		Enabled: true,
	})
	if rr := doBearerRequest(t, env.srv, userPAT, http.MethodGet, "/api/agents/"+restrictedID, nil); rr.Code != http.StatusForbidden {
		t.Fatalf("normal PAT GET admin-owned restricted agent: want 403, got %d (%s)", rr.Code, rr.Body.String())
	}
}

// TestPAT_Lifecycle covers create-time expiry policy, one-time plaintext, and
// input validation through the real handler.
func TestPAT_Lifecycle(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Default expiry: no expires_at/never_expires -> ~90 days.
	plaintext, id := mintPAT(t, env, env.bearerToken, "life_default")
	var exp *time.Time
	var last4 string
	var scopes []string
	if err := env.db.QueryRow(ctx,
		"select expires_at, last4, scopes from personal_access_token where id=$1", id).Scan(&exp, &last4, &scopes); err != nil {
		t.Fatalf("query default token: %v", err)
	}
	if exp == nil {
		t.Fatal("default PAT must carry an expiry")
	}
	if d := time.Until(*exp); d < 89*24*time.Hour || d > 91*24*time.Hour {
		t.Fatalf("default expiry want ~90d, got %v", d)
	}
	if last4 != plaintext[len(plaintext)-4:] {
		t.Fatalf("last4 = %q, want suffix of plaintext %q", last4, plaintext)
	}
	if len(scopes) != 0 {
		t.Fatalf("new PAT scopes = %v, want empty legacy storage value", scopes)
	}

	// never_expires -> NULL expiry.
	rr := doRequest(t, env, http.MethodPost, "/api/users/me/tokens",
		map[string]any{"name": "life_never", "never_expires": true})
	if rr.Code != http.StatusCreated {
		t.Fatalf("never_expires create: status = %d (%s)", rr.Code, rr.Body.String())
	}
	var neverResp struct {
		PAT struct {
			ID string `json:"id"`
		} `json:"personal_access_token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &neverResp); err != nil {
		t.Fatalf("decode never: %v", err)
	}
	var neverExp *time.Time
	if err := env.db.QueryRow(ctx,
		"select expires_at from personal_access_token where id=$1", neverResp.PAT.ID).Scan(&neverExp); err != nil {
		t.Fatalf("query never token: %v", err)
	}
	if neverExp != nil {
		t.Fatalf("never_expires must store NULL expiry, got %v", neverExp)
	}

	// Name and expiry validation remain unchanged; obsolete scope fields are
	// ignored for compatibility with older callers.
	bad := []struct {
		name string
		body map[string]any
	}{
		{"blank name", map[string]any{"name": ""}},
		{"past expiry", map[string]any{"name": "x", "expires_at": "2020-01-01T00:00:00Z"}},
		{"beyond max lifetime", map[string]any{
			"name":       "x",
			"expires_at": time.Now().Add(400 * 24 * time.Hour).UTC().Format(time.RFC3339),
		}},
	}
	for _, b := range bad {
		t.Run(b.name, func(t *testing.T) {
			rr := doRequest(t, env, http.MethodPost, "/api/users/me/tokens", b.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", rr.Code, rr.Body.String())
			}
		})
	}
}

// TestPAT_OwnershipAndRevoke proves ownership isolation (a token is invisible to
// other users, 404 before existence leaks), revocation, post-revoke rejection at
// the resolver, and expiry enforcement.
func TestPAT_OwnershipAndRevoke(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// A second user's PAT remains visible only to its own owner.
	_, u2Bearer := createTestUserWithToken(t, env.authStore, env.oidcStore, "patuser2", auth.RoleUser)
	u2PAT, u2PATID := mintPAT(t, env, u2Bearer, "u2_tok")
	if rr := doBearerRequest(t, env.srv, u2PAT, http.MethodGet, "/api/users/me/tokens/"+u2PATID, nil); rr.Code != http.StatusOK {
		t.Fatalf("PAT GET own token: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	_, otherBearer := createTestUserWithToken(t, env.authStore, env.oidcStore, "patuser3", auth.RoleUser)
	otherPAT, _ := mintPAT(t, env, otherBearer, "u3_tok")
	if rr := doBearerRequest(t, env.srv, otherPAT, http.MethodGet, "/api/users/me/tokens/"+u2PATID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("PAT GET other user's token: want 404, got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doBearerRequest(t, env.srv, otherPAT, http.MethodDelete, "/api/users/me/tokens/"+u2PATID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("PAT DELETE other user's token: want 404, got %d", rr.Code)
	}

	// Revoke invalidates the token at the auth boundary.
	tok, id := mintPAT(t, env, env.bearerToken, "revoke_me")
	if rr := doBearerRequest(t, env.srv, tok, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusOK {
		t.Fatalf("pre-revoke agents: want 200, got %d", rr.Code)
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/users/me/tokens/"+id, nil); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke: want 204, got %d (%s)", rr.Code, rr.Body.String())
	}
	var revokedAt *time.Time
	if err := env.db.QueryRow(ctx, "select revoked_at from personal_access_token where id=$1", id).Scan(&revokedAt); err != nil {
		t.Fatalf("query revoked_at: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("revoke must set revoked_at")
	}
	if rr := doBearerRequest(t, env.srv, tok, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("post-revoke agents: want 401, got %d", rr.Code)
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/users/me/tokens/"+id, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("double revoke: want 404, got %d", rr.Code)
	}

	// A past expiry is rejected by the resolver (401), same as revocation.
	tok2, id2 := mintPAT(t, env, env.bearerToken, "expire_me")
	if _, err := env.db.Exec(ctx,
		"update personal_access_token set expires_at = now() - interval '1 hour' where id=$1", id2); err != nil {
		t.Fatalf("force-expire: %v", err)
	}
	if rr := doBearerRequest(t, env.srv, tok2, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: want 401, got %d", rr.Code)
	}
}
