package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
)

// The admin scope override round-trips over HTTP: PUT persists a normalized
// (deduped) scope list surfaced alongside default_scopes; GET returns it;
// DELETE reverts to defaults-only (D7).
func TestAdminOAuthProviderConfigScopeRoundTrip(t *testing.T) {
	env := setupAdmin(t)

	reg := oauth.NewProviderRegistry()
	reg.Register(oauth.ProviderConfig{
		ID: "github", VaultKey: oauth.VaultKeyGitHub, ClientID: "yaml-client", Scopes: []string{"repo"}, AllowedScopes: []string{"repo", "read:org"},
	})
	env.credSvc.SetRegistry(reg)

	type configResp struct {
		ProviderID           string   `json:"provider_id"`
		ClientID             string   `json:"client_id"`
		Scopes               []string `json:"scopes"`
		DefaultScopes        []string `json:"default_scopes"`
		AllowedScopes        []string `json:"allowed_scopes"`
		DefaultAllowedScopes []string `json:"default_allowed_scopes"`
	}
	decode := func(rr *httptest.ResponseRecorder) configResp {
		t.Helper()
		var out configResp
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode config: %v (body=%s)", err, rr.Body.String())
		}
		return out
	}

	const path = "/api/admin/oauth-providers/github/config"

	// PUT a scope override with a duplicate that must be deduped.
	put := doRequest(t, env, http.MethodPut, path, map[string]any{
		"client_id":      "admin-client",
		"client_secret":  "",
		"scopes":         []string{"repo", "read:org", "repo"},
		"allowed_scopes": []string{"repo", "read:org", "workflow", "workflow"},
	})
	if put.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200 (body=%s)", put.Code, put.Body.String())
	}
	got := decode(put)
	if !reflect.DeepEqual(got.Scopes, []string{"repo", "read:org"}) {
		t.Errorf("PUT scopes = %v, want [repo read:org]", got.Scopes)
	}
	if !reflect.DeepEqual(got.DefaultScopes, []string{"repo"}) {
		t.Errorf("PUT default_scopes = %v, want [repo]", got.DefaultScopes)
	}
	if !reflect.DeepEqual(got.AllowedScopes, []string{"repo", "read:org", "workflow"}) {
		t.Errorf("PUT allowed_scopes = %v, want [repo read:org workflow]", got.AllowedScopes)
	}
	if !reflect.DeepEqual(got.DefaultAllowedScopes, []string{"repo", "read:org"}) {
		t.Errorf("PUT default_allowed_scopes = %v, want [repo read:org]", got.DefaultAllowedScopes)
	}

	// GET returns the same override plus defaults.
	get := doRequest(t, env, http.MethodGet, path, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", get.Code)
	}
	got = decode(get)
	if !reflect.DeepEqual(got.Scopes, []string{"repo", "read:org"}) {
		t.Errorf("GET scopes = %v, want [repo read:org]", got.Scopes)
	}

	// DELETE reverts to defaults-only.
	del := doRequest(t, env, http.MethodDelete, path, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want 204", del.Code)
	}
	after := decode(doRequest(t, env, http.MethodGet, path, nil))
	if len(after.Scopes) != 0 {
		t.Errorf("after DELETE scopes = %v, want empty", after.Scopes)
	}
	if !reflect.DeepEqual(after.DefaultScopes, []string{"repo"}) {
		t.Errorf("after DELETE default_scopes = %v, want [repo]", after.DefaultScopes)
	}
}
