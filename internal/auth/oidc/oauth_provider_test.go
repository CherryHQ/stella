package oidc

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestOAuthProviderFeishuLoginAndCallback(t *testing.T) {
	var tokenPath, userInfoPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			if r.Method != http.MethodPost {
				t.Fatalf("token method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token request: %v", err)
			}
			if body["code"] != "auth-code" || body["code_verifier"] != "verifier" {
				t.Fatalf("token request = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0, "access_token": "user-token", "token_type": "Bearer",
				"refresh_token": "refresh-token", "expires_in": 7200, "refresh_token_expires_in": 2592000,
			})
		case userInfoPath:
			if got := r.Header.Get("Authorization"); got != "Bearer user-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"union_id":   "on_union",
					"open_id":    "ou_open",
					"tenant_key": "tenant-1",
					"email":      "user@example.com",
					"name":       "Feishu User",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	tokenPath = "/token"
	userInfoPath = "/userinfo"

	cfg := &OAuthConfig{
		ProviderName:        "feishu",
		Kind:                "feishu",
		ClientID:            "client-id",
		ClientSecret:        "client-secret",
		RedirectURL:         "https://stella.example/auth/callback/feishu",
		Scopes:              []string{"contact:user.email:readonly"},
		AuthURL:             server.URL + "/authorize",
		TokenURL:            server.URL + tokenPath,
		TokenRequestStyle:   "json",
		UserInfoURL:         server.URL + userInfoPath,
		AllowedTenantKeys:   []string{"tenant-1"},
		AllowedEmailDomains: []string{"example.com"},
	}
	p, err := NewOAuthProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}

	loginURL, err := p.LoginURL(t.Context(), auth.AuthState{State: "state", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("client_id") != "client-id" || q.Get("state") != "state" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("bad login query: %s", loginURL)
	}
	if !strings.Contains(q.Get("scope"), "contact:user.email:readonly") {
		t.Fatalf("scope = %q", q.Get("scope"))
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
	identity, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Provider != "feishu" || identity.Subject != "on_union" || identity.Email != "user@example.com" || identity.Name != "Feishu User" {
		t.Fatalf("identity = %#v", identity)
	}
	if identity.OAuthToken == nil {
		t.Fatal("OAuthToken is nil")
	}
	if identity.OAuthToken.AccessToken != "user-token" {
		t.Fatalf("AccessToken = %q", identity.OAuthToken.AccessToken)
	}
	if identity.OAuthToken.RefreshToken != "refresh-token" {
		t.Fatalf("RefreshToken = %q", identity.OAuthToken.RefreshToken)
	}
	if identity.OAuthToken.ExpiresIn != 7200 {
		t.Fatalf("ExpiresIn = %d", identity.OAuthToken.ExpiresIn)
	}
	if identity.OAuthToken.RefreshTokenExpiresIn != 2592000 {
		t.Fatalf("RefreshTokenExpiresIn = %d", identity.OAuthToken.RefreshTokenExpiresIn)
	}
}

func TestOAuthProviderFeishuAllowsMissingEmailWhenTenantAllowed(t *testing.T) {
	var tokenPath, userInfoPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "access_token": "user-token"})
		case userInfoPath:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"union_id":   "on_union",
					"tenant_key": "tenant-1",
					"name":       "Feishu User",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	tokenPath = "/token"
	userInfoPath = "/userinfo"

	cfg := &OAuthConfig{
		ProviderName:      "feishu",
		Kind:              "feishu",
		ClientID:          "client-id",
		ClientSecret:      "client-secret",
		RedirectURL:       "https://stella.example/auth/callback/feishu",
		AuthURL:           server.URL + "/authorize",
		TokenURL:          server.URL + tokenPath,
		TokenRequestStyle: "json",
		UserInfoURL:       server.URL + userInfoPath,
		AllowedTenantKeys: []string{"tenant-1"},
	}
	p, err := NewOAuthProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
	identity, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != "on_union@tenant-1.feishu.local" {
		t.Fatalf("Email = %q", identity.Email)
	}
	if identity.Claims["email_synthetic"] != true {
		t.Fatalf("email_synthetic claim = %#v", identity.Claims["email_synthetic"])
	}
}

func TestOAuthProviderFeishuRejectsMissingUnionID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"open_id": "ou_open", "tenant_key": "tenant-1"},
		})
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName: "feishu", Kind: "feishu", ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURL: "https://stella.example/auth/callback/feishu", AuthURL: server.URL + "/authorize",
		TokenURL: server.URL + "/token", TokenRequestStyle: "json", UserInfoURL: server.URL,
		AllowedTenantKeys: []string{"tenant-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.fetchFeishuProfile(t.Context(), "token"); err == nil || !strings.Contains(err.Error(), "missing union_id") {
		t.Fatalf("fetchFeishuProfile error = %v, want missing union_id", err)
	}
}

func TestOAuthProviderGenericRequiresEmailVerifiedByDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":   "user-1",
			"email": "user@example.com",
			"name":  "User",
		})
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:         "acme",
		Kind:                 "generic",
		ClientID:             "client-id",
		ClientSecret:         "client-secret",
		RedirectURL:          "https://stella.example/auth/callback/acme",
		AuthURL:              server.URL + "/authorize",
		TokenURL:             server.URL + "/token",
		UserInfoURL:          server.URL,
		TokenRequestStyle:    "form",
		AllowedEmailDomains:  []string{"example.com"},
		RequireEmailVerified: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.fetchGenericProfile(t.Context(), "token"); err == nil {
		t.Fatal("expected missing email_verified to be rejected")
	}
}

func TestOAuthProviderGenericCanTrustProviderEmail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"sub":   "user-1",
			"email": "user@example.com",
			"name":  "User",
		})
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:         "acme",
		Kind:                 "generic",
		ClientID:             "client-id",
		ClientSecret:         "client-secret",
		RedirectURL:          "https://stella.example/auth/callback/acme",
		AuthURL:              server.URL + "/authorize",
		TokenURL:             server.URL + "/token",
		UserInfoURL:          server.URL,
		TokenRequestStyle:    "form",
		AllowedEmailDomains:  []string{"example.com"},
		RequireEmailVerified: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := p.fetchGenericProfile(t.Context(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if !profile.EmailVerified {
		t.Fatal("expected trusted provider email to be treated as verified")
	}
}

func TestBoolClaimAcceptsStringTrue(t *testing.T) {
	if !boolClaim(map[string]any{"email_verified": "true"}, "email_verified") {
		t.Fatal("expected string true to be accepted")
	}
}

func TestOAuthProviderRejectsDisallowedFeishuTenant(t *testing.T) {
	cfg := &OAuthConfig{
		ProviderName:      "feishu",
		Kind:              "feishu",
		ClientID:          "client-id",
		ClientSecret:      "client-secret",
		RedirectURL:       "https://stella.example/auth/callback/feishu",
		AuthURL:           "https://accounts.feishu.cn/open-apis/authen/v1/authorize",
		TokenURL:          "https://open.feishu.cn/open-apis/authen/v2/oauth/token",
		TokenRequestStyle: "json",
		UserInfoURL:       "https://open.feishu.cn/open-apis/authen/v1/user_info",
		AllowedTenantKeys: []string{"tenant-1"},
	}
	p, err := NewOAuthProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	err = p.checkAllowed(&oauthProfile{TenantKey: "tenant-2", Email: "user@example.com", EmailVerified: true})
	if err == nil {
		t.Fatal("expected disallowed tenant error")
	}
}
