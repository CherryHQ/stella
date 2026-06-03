package oidc

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/auth"
)

func TestOAuthProviderFeishuLoginAndCallback(t *testing.T) {
	var appTokenPath, profileTokenPath, userInfoPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case appTokenPath:
			if r.Method != http.MethodPost {
				t.Fatalf("app token method = %s", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode app token request: %v", err)
			}
			if body["app_id"] != "client-id" || body["app_secret"] != "client-secret" {
				t.Fatalf("app token request = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "app_access_token": "app-token", "expire": 7200})
		case profileTokenPath:
			if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
				t.Fatalf("Authorization = %q", got)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode profile token request: %v", err)
			}
			if body["grant_type"] != "authorization_code" || body["code"] != "auth-code" {
				t.Fatalf("profile token request = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"access_token": "user-token",
					"union_id":     "on_union",
					"open_id":      "ou_open",
					"tenant_key":   "tenant-1",
					"email":        "user@example.com",
					"name":         "Feishu User",
				},
			})
		case userInfoPath:
			t.Fatal("fast Feishu login should not call user_info")
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	appTokenPath = "/app-token"
	profileTokenPath = "/profile-token"
	userInfoPath = "/userinfo"

	cfg := &OAuthConfig{
		ProviderName:              "feishu",
		Kind:                      "feishu",
		ClientID:                  "client-id",
		ClientSecret:              "client-secret",
		RedirectURL:               "https://stella.example/auth/callback/feishu",
		Scopes:                    []string{"contact:user.email:readonly"},
		AuthURL:                   server.URL + "/authorize",
		TokenURL:                  server.URL + "/legacy-token",
		TokenRequestStyle:         "json",
		UserInfoURL:               server.URL + userInfoPath,
		FeishuProfileTokenEnabled: true,
		FeishuProfileTokenURL:     server.URL + profileTokenPath,
		FeishuAppTokenURL:         server.URL + appTokenPath,
		AllowedTenantKeys:         []string{"tenant-1"},
		AllowedEmailDomains:       []string{"example.com"},
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
}

func TestOAuthProviderFeishuAllowsMissingEmailWhenTenantAllowed(t *testing.T) {
	var appTokenPath, profileTokenPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case appTokenPath:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "app_access_token": "app-token", "expire": 7200})
		case profileTokenPath:
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
	appTokenPath = "/app-token"
	profileTokenPath = "/profile-token"

	cfg := &OAuthConfig{
		ProviderName:              "feishu",
		Kind:                      "feishu",
		ClientID:                  "client-id",
		ClientSecret:              "client-secret",
		RedirectURL:               "https://stella.example/auth/callback/feishu",
		AuthURL:                   server.URL + "/authorize",
		TokenURL:                  server.URL + "/legacy-token",
		TokenRequestStyle:         "json",
		UserInfoURL:               server.URL + "/userinfo",
		FeishuProfileTokenEnabled: true,
		FeishuProfileTokenURL:     server.URL + profileTokenPath,
		FeishuAppTokenURL:         server.URL + appTokenPath,
		AllowedTenantKeys:         []string{"tenant-1"},
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

func TestOAuthProviderFeishuCachesAppToken(t *testing.T) {
	var appTokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app-token":
			appTokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"app_access_token": "app-token", "expire": 7200}})
		case "/profile-token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"union_id":   "on_union",
					"tenant_key": "tenant-1",
					"email":      "user@example.com",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:              "feishu",
		Kind:                      "feishu",
		ClientID:                  "client-id",
		ClientSecret:              "client-secret",
		RedirectURL:               "https://stella.example/auth/callback/feishu",
		AuthURL:                   server.URL + "/authorize",
		TokenURL:                  server.URL + "/legacy-token",
		TokenRequestStyle:         "json",
		UserInfoURL:               server.URL + "/userinfo",
		FeishuProfileTokenEnabled: true,
		FeishuProfileTokenURL:     server.URL + "/profile-token",
		FeishuAppTokenURL:         server.URL + "/app-token",
		AllowedTenantKeys:         []string{"tenant-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	for range 2 {
		req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
		if _, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"}); err != nil {
			t.Fatal(err)
		}
	}
	if appTokenCalls != 1 {
		t.Fatalf("appTokenCalls = %d, want 1", appTokenCalls)
	}
}

func TestOAuthProviderFeishuRefreshesExpiredAppToken(t *testing.T) {
	var appTokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app-token":
			appTokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "app_access_token": "app-token", "expire": 7200})
		case "/profile-token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"union_id":   "on_union",
					"tenant_key": "tenant-1",
					"email":      "user@example.com",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:              "feishu",
		Kind:                      "feishu",
		ClientID:                  "client-id",
		ClientSecret:              "client-secret",
		RedirectURL:               "https://stella.example/auth/callback/feishu",
		AuthURL:                   server.URL + "/authorize",
		TokenURL:                  server.URL + "/legacy-token",
		TokenRequestStyle:         "json",
		UserInfoURL:               server.URL + "/userinfo",
		FeishuProfileTokenEnabled: true,
		FeishuProfileTokenURL:     server.URL + "/profile-token",
		FeishuAppTokenURL:         server.URL + "/app-token",
		AllowedTenantKeys:         []string{"tenant-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
	if _, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"}); err != nil {
		t.Fatal(err)
	}
	p.feishuAppTokenExpiresAt = time.Now().Add(-time.Second)
	req = httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
	if _, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"}); err != nil {
		t.Fatal(err)
	}
	if appTokenCalls != 2 {
		t.Fatalf("appTokenCalls = %d, want 2", appTokenCalls)
	}
}

func TestOAuthProviderFeishuInvalidAppTokenRetriesOnce(t *testing.T) {
	var appTokenCalls, profileTokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app-token":
			appTokenCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "app_access_token": "app-token-" + fmt.Sprint(appTokenCalls), "expire": 7200})
		case "/profile-token":
			profileTokenCalls++
			if got := r.Header.Get("Authorization"); profileTokenCalls == 1 && got != "Bearer app-token-1" {
				t.Fatalf("first Authorization = %q", got)
			} else if profileTokenCalls == 2 && got != "Bearer app-token-2" {
				t.Fatalf("second Authorization = %q", got)
			}
			if profileTokenCalls == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 99991663, "msg": "invalid app access token"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"union_id":   "on_union",
					"tenant_key": "tenant-1",
					"email":      "user@example.com",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:              "feishu",
		Kind:                      "feishu",
		ClientID:                  "client-id",
		ClientSecret:              "client-secret",
		RedirectURL:               "https://stella.example/auth/callback/feishu",
		AuthURL:                   server.URL + "/authorize",
		TokenURL:                  server.URL + "/legacy-token",
		TokenRequestStyle:         "json",
		UserInfoURL:               server.URL + "/userinfo",
		FeishuProfileTokenEnabled: true,
		FeishuProfileTokenURL:     server.URL + "/profile-token",
		FeishuAppTokenURL:         server.URL + "/app-token",
		AllowedTenantKeys:         []string{"tenant-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
	if _, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"}); err != nil {
		t.Fatal(err)
	}
	if appTokenCalls != 2 || profileTokenCalls != 2 {
		t.Fatalf("appTokenCalls=%d profileTokenCalls=%d, want 2/2", appTokenCalls, profileTokenCalls)
	}
}

func TestFeishuAppTokenExpiresAtHonorsShortTTL(t *testing.T) {
	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	if got, want := feishuAppTokenExpiresAt(now, 200), now.Add(100*time.Second); !got.Equal(want) {
		t.Fatalf("short TTL expiresAt = %s, want %s", got, want)
	}
	if got, want := feishuAppTokenExpiresAt(now, 7200), now.Add(6900*time.Second); !got.Equal(want) {
		t.Fatalf("normal TTL expiresAt = %s, want %s", got, want)
	}
	if got := feishuAppTokenExpiresAt(now, 0); !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("default expiresAt = %s, want %s", got, now.Add(time.Hour))
	}
}

func TestOAuthProviderDoJSONOmitsHTTPErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"access_token":"secret-token"}`))
	}))
	defer server.Close()

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:        "acme",
		Kind:                "generic",
		ClientID:            "client-id",
		ClientSecret:        "client-secret",
		RedirectURL:         "https://stella.example/auth/callback/acme",
		AuthURL:             server.URL + "/authorize",
		TokenURL:            server.URL + "/token",
		UserInfoURL:         server.URL + "/userinfo",
		TokenRequestStyle:   "form",
		AllowedEmailDomains: []string{"example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.doJSON(req, &map[string]any{}); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("doJSON error = %v", err)
	}
}

func TestOAuthProviderFeishuCanUseLegacyUserInfoFlow(t *testing.T) {
	var tokenPath, userInfoPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case tokenPath:
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token request: %v", err)
			}
			if body["code"] != "auth-code" || body["code_verifier"] != "verifier" {
				t.Fatalf("token request = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "access_token": "user-token", "token_type": "Bearer"})
		case userInfoPath:
			if got := r.Header.Get("Authorization"); got != "Bearer user-token" {
				t.Fatalf("Authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"union_id":   "on_union",
					"tenant_key": "tenant-1",
					"email":      "user@example.com",
				},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	tokenPath = "/token"
	userInfoPath = "/userinfo"

	p, err := NewOAuthProvider(&OAuthConfig{
		ProviderName:              "feishu",
		Kind:                      "feishu",
		ClientID:                  "client-id",
		ClientSecret:              "client-secret",
		RedirectURL:               "https://stella.example/auth/callback/feishu",
		AuthURL:                   server.URL + "/authorize",
		TokenURL:                  server.URL + tokenPath,
		TokenRequestStyle:         "json",
		UserInfoURL:               server.URL + userInfoPath,
		FeishuProfileTokenEnabled: false,
		AllowedTenantKeys:         []string{"tenant-1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/callback?code=auth-code&state=state", nil)
	identity, err := p.HandleCallback(t.Context(), req, auth.AuthState{State: "state", CodeVerifier: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "on_union" || identity.Email != "user@example.com" {
		t.Fatalf("identity = %#v", identity)
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
