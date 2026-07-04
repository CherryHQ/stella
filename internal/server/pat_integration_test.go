package server_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

// These integration tests drive the real server mux (middleware Resolve+Enforce
// -> generated handlers -> Postgres) for Personal Access Tokens. They cover the
// wired-up path the internal/credential unit tests cannot reach: a PAT minted
// through the create endpoint, presented as a Bearer against real routes, and
// the resulting rows in personal_access_token. The admin's legacy full-access
// bearer (env.bearerToken) is the driver for management calls, since a PAT is
// deliberately not allowed to manage PATs.

// mintPAT creates a PAT via POST /api/users/me/tokens using the given bearer and
// returns the one-time plaintext plus the token id.
func mintPAT(t *testing.T, env *testEnv, bearer, name string, scopes []string) (plaintext, id string) {
	t.Helper()
	rr := doBearerRequest(t, env.srv, bearer, http.MethodPost, "/api/users/me/tokens",
		map[string]any{"name": name, "scopes": scopes})
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

// TestPAT_EnforceMatrix proves scope + reachability enforcement end to end: the
// allow path serves 200, and every denial mode returns the right status through
// the real middleware, not just the unit-tested Enforce function.
func TestPAT_EnforceMatrix(t *testing.T) {
	env := setupAdmin(t)
	// agent:read reaches a route the test harness fully wires (GET /api/agents,
	// seeded), so a 200 proves Enforce passed AND a handler served. The denial
	// cases below turn on scope mismatch alone, independent of backend wiring.
	tok, _ := mintPAT(t, env, env.bearerToken, "enforce_agent", []string{"agent:read"})
	wild, _ := mintPAT(t, env, env.bearerToken, "enforce_wild", []string{"agent:*"})

	cases := []struct {
		name         string
		token        string
		method, path string
		want         int
	}{
		{"allow agent:read", tok, http.MethodGet, "/api/agents", http.StatusOK},
		{"wildcard agent:*", wild, http.MethodGet, "/api/agents", http.StatusOK},
		{"public status exempt", tok, http.MethodGet, "/api/status", http.StatusOK},
		{"missing scope", tok, http.MethodGet, "/api/shares", http.StatusForbidden},
		{"read token cannot write", tok, http.MethodPost, "/api/goals", http.StatusForbidden},
		{"vault not PAT-exposable", tok, http.MethodGet, "/api/vault", http.StatusForbidden},
		{"oauth not PAT-exposable", tok, http.MethodGet, "/api/users/me/oauth", http.StatusForbidden},
		{"PAT cannot manage PATs", tok, http.MethodGet, "/api/users/me/tokens", http.StatusForbidden},
		{"token-scopes denied", tok, http.MethodGet, "/api/token-scopes", http.StatusForbidden},
		{"denied resource models", tok, http.MethodGet, "/api/models", http.StatusForbidden},
		{"non-api page route", tok, http.MethodGet, "/agents", http.StatusForbidden},
		{"unregistered route fail-closed", tok, http.MethodGet, "/api/nonexistent-xyz", http.StatusForbidden},
		{"malformed pat hard-denied", "stella_pat_deadbeef_notarealsecret", http.MethodGet, "/api/goals", http.StatusUnauthorized},
		{"reserved oat hard-denied", "stella_oat_sometoken", http.MethodGet, "/api/goals", http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := doBearerRequest(t, env.srv, c.token, c.method, c.path, nil)
			if rr.Code != c.want {
				t.Fatalf("%s %s: status = %d, want %d (body %s)", c.method, c.path, rr.Code, c.want, rr.Body.String())
			}
		})
	}
}

// TestPAT_Lifecycle covers create-time expiry policy, one-time plaintext, and
// input validation through the real handler.
func TestPAT_Lifecycle(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()

	// Default expiry: no expires_at/never_expires -> ~90 days.
	plaintext, id := mintPAT(t, env, env.bearerToken, "life_default", []string{"goals:read"})
	var exp *time.Time
	var last4 string
	if err := env.db.QueryRow(ctx,
		"select expires_at, last4 from personal_access_token where id=$1", id).Scan(&exp, &last4); err != nil {
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

	// never_expires -> NULL expiry.
	rr := doRequest(t, env, http.MethodPost, "/api/users/me/tokens",
		map[string]any{"name": "life_never", "scopes": []string{"goals:read"}, "never_expires": true})
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

	// Rejected inputs -> 400, nothing persisted.
	bad := []struct {
		name string
		body map[string]any
	}{
		{"vault scope", map[string]any{"name": "x", "scopes": []string{"vault:read"}}},
		{"oauth scope", map[string]any{"name": "x", "scopes": []string{"oauth:*"}}},
		{"bad action", map[string]any{"name": "x", "scopes": []string{"goals:delete"}}},
		{"unknown resource", map[string]any{"name": "x", "scopes": []string{"bogus:read"}}},
		{"empty scopes", map[string]any{"name": "x", "scopes": []string{}}},
		{"blank name", map[string]any{"name": "", "scopes": []string{"goals:read"}}},
		{"past expiry", map[string]any{"name": "x", "scopes": []string{"goals:read"}, "expires_at": "2020-01-01T00:00:00Z"}},
		{"beyond max lifetime", map[string]any{
			"name": "x", "scopes": []string{"goals:read"},
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

	// A second user's PAT must be invisible to the admin (404, not 200/403).
	_, u2Bearer := createTestUserWithToken(t, env.authStore, env.oidcStore, "patuser2", auth.RoleUser)
	_, u2PATID := mintPAT(t, env, u2Bearer, "u2_tok", []string{"goals:read"})
	if rr := doRequest(t, env, http.MethodGet, "/api/users/me/tokens/"+u2PATID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("admin GET other user's token: want 404, got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodDelete, "/api/users/me/tokens/"+u2PATID, nil); rr.Code != http.StatusNotFound {
		t.Fatalf("admin DELETE other user's token: want 404, got %d", rr.Code)
	}

	// Revoke invalidates the token at the auth boundary.
	tok, id := mintPAT(t, env, env.bearerToken, "revoke_me", []string{"agent:read"})
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
	tok2, id2 := mintPAT(t, env, env.bearerToken, "expire_me", []string{"agent:read"})
	if _, err := env.db.Exec(ctx,
		"update personal_access_token set expires_at = now() - interval '1 hour' where id=$1", id2); err != nil {
		t.Fatalf("force-expire: %v", err)
	}
	if rr := doBearerRequest(t, env.srv, tok2, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: want 401, got %d", rr.Code)
	}
}

// TestPAT_ScopeCatalog asserts the grantable-scope catalog served to the UI
// exposes the PAT-reachable resources and never the sandbox-internal ones.
func TestPAT_ScopeCatalog(t *testing.T) {
	env := setupAdmin(t)
	rr := doRequest(t, env, http.MethodGet, "/api/token-scopes", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("token-scopes: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var resp struct {
		Scopes []struct {
			ID string `json:"id"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode scopes: %v", err)
	}
	ids := make(map[string]bool, len(resp.Scopes))
	for _, s := range resp.Scopes {
		ids[s.ID] = true
	}
	for _, want := range []string{"goals:*", "goals:read", "shares:*", "skills:read"} {
		if !ids[want] {
			t.Errorf("scope catalog missing %q", want)
		}
	}
	for _, forbidden := range []string{"vault:read", "vault:*", "oauth:read", "oauth:*"} {
		if ids[forbidden] {
			t.Errorf("scope catalog must not expose sandbox-internal %q", forbidden)
		}
	}
}
