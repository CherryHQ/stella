package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc"
	"github.com/CherryHQ/stella/internal/server"
)

func TestOAuthPATSmoke_E2E(t *testing.T) {
	env := setupAdmin(t)

	clientID, secret := registerOAuthClientAPI(t, env, "https://app.example/cb", []string{"agent:read"})

	code := authorizeOAuthClient(t, env.srv, env.bearerToken, clientID, "https://app.example/cb", "agent:read")
	first := exchangeOAuthToken(t, env.srv, url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"code":          {code},
		"redirect_uri":  {"https://app.example/cb"},
	})
	if rr := doBearerRequest(t, env.srv, first.AccessToken, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusOK {
		t.Fatalf("oauth access token GET /api/agents: status = %d, body = %s", rr.Code, rr.Body.String())
	}

	rotated := exchangeOAuthToken(t, env.srv, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"refresh_token": {first.RefreshToken},
	})
	if rotated.RefreshToken == first.RefreshToken || rotated.AccessToken == first.AccessToken {
		t.Fatal("refresh rotation must issue new access and refresh tokens")
	}

	reuse := postForm(t, env.srv, "/oauth/token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"client_secret": {secret},
		"refresh_token": {first.RefreshToken},
	}, "")
	if reuse.Code != http.StatusBadRequest {
		t.Fatalf("refresh reuse: want 400, got %d (%s)", reuse.Code, reuse.Body.String())
	}
	if rr := doBearerRequest(t, env.srv, rotated.AccessToken, http.MethodGet, "/api/agents", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("family-revoked access token: want 401, got %d (%s)", rr.Code, rr.Body.String())
	}
}

func TestOAuthClientManagementSmoke_E2E(t *testing.T) {
	env := setupAdmin(t)

	badRedirects := []string{"//evil.example/cb", "/cb", "javascript:alert(1)", "http://evil.example/cb"}
	for _, redirectURI := range badRedirects {
		rr := doRequest(t, env, http.MethodPost, "/api/users/me/oauth-clients", map[string]any{
			"name": "bad", "redirect_uris": []string{redirectURI}, "scopes": []string{"agent:read"},
		})
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("redirect_uri %q: want 400, got %d (%s)", redirectURI, rr.Code, rr.Body.String())
		}
	}

	publicID, _ := registerOAuthClientAPI(t, env, "http://127.0.0.1:8080/cb", []string{"agent:read"}, "public")
	if rr := doRequest(t, env, http.MethodPost, "/api/users/me/oauth-clients/missing/rotate-secret", nil); rr.Code != http.StatusNotFound {
		t.Fatalf("rotate missing client: want 404, got %d (%s)", rr.Code, rr.Body.String())
	}
	if rr := doRequest(t, env, http.MethodPost, "/api/users/me/oauth-clients/"+publicID+"/rotate-secret", nil); rr.Code != http.StatusBadRequest {
		t.Fatalf("rotate public client: want 400, got %d (%s)", rr.Code, rr.Body.String())
	}

	confidentialID, oldSecret := registerOAuthClientAPI(t, env, "https://rotate.example/cb", []string{"agent:read"})
	rr := doRequest(t, env, http.MethodPost, "/api/users/me/oauth-clients/"+confidentialID+"/rotate-secret", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("rotate confidential client: want 200, got %d (%s)", rr.Code, rr.Body.String())
	}
	var rotated struct {
		ClientSecret string `json:"client_secret"`
	}
	decodeJSONBody(t, rr, &rotated)
	if rotated.ClientSecret == "" || rotated.ClientSecret == oldSecret {
		t.Fatalf("rotate confidential client returned bad secret: %q", rotated.ClientSecret)
	}
}

func TestEmptyBearerWithSessionHardDenied_E2E(t *testing.T) {
	env := setupAdmin(t)
	ctx := context.Background()
	authSvc := auth.NewAuthService(env.db, env.oidcStore, env.oidcStore, env.oidcStore)
	sessionMgr, err := auth.NewSessionManager(env.oidcStore, "test-vault-key-32bytes!!!!!!!!")
	if err != nil {
		t.Fatalf("NewSessionManager: %v", err)
	}
	env.srv.SetOIDCAuth(&oidc.SetupResult{AuthSvc: authSvc, SessionMgr: sessionMgr})
	login, err := authSvc.ProcessOIDCLogin(ctx, auth.ExternalIdentity{
		Provider: "test", Subject: "empty-bearer", Email: "empty-bearer@test.local", Name: "Empty Bearer",
	}, sessionMgr)
	if err != nil {
		t.Fatalf("ProcessOIDCLogin: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.Header.Set("Authorization", "Bearer")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: login.SessionToken})
	rr := httptest.NewRecorder()
	env.srv.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("empty bearer with valid session must hard-deny: got %d (%s)", rr.Code, rr.Body.String())
	}
}

func registerOAuthClientAPI(t *testing.T, env *testEnv, redirectURI string, scopes []string, clientTypes ...string) (clientID, secret string) {
	t.Helper()
	body := map[string]any{"name": "e2e", "redirect_uris": []string{redirectURI}, "scopes": scopes}
	if len(clientTypes) > 0 {
		body["client_type"] = clientTypes[0]
	}
	rr := doRequest(t, env, http.MethodPost, "/api/users/me/oauth-clients", body)
	if rr.Code != http.StatusCreated {
		t.Fatalf("register oauth client: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ClientSecret string `json:"client_secret"`
		OAuthClient  struct {
			ClientID string `json:"client_id"`
		} `json:"oauth_client"`
	}
	decodeJSONBody(t, rr, &resp)
	if resp.OAuthClient.ClientID == "" {
		t.Fatalf("register oauth client returned no client_id: %s", rr.Body.String())
	}
	return resp.OAuthClient.ClientID, resp.ClientSecret
}

func authorizeOAuthClient(t *testing.T, srv *server.Server, bearer, clientID, redirectURI, scope string) string {
	t.Helper()
	form := url.Values{
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {scope},
		"state":         {"s1"},
		"consent":       {"approve"},
	}
	rr := postForm(t, srv, "/oauth/authorize", form, bearer)
	if rr.Code != http.StatusFound {
		t.Fatalf("authorize: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse authorize redirect %q: %v", loc, err)
	}
	if got := u.Query().Get("state"); got != "s1" {
		t.Fatalf("authorize state = %q, want s1", got)
	}
	code := u.Query().Get("code")
	if code == "" {
		t.Fatalf("authorize redirect missing code: %q", loc)
	}
	return code
}

type oauthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func exchangeOAuthToken(t *testing.T, srv *server.Server, form url.Values) oauthTokenResponse {
	t.Helper()
	rr := postForm(t, srv, "/oauth/token", form, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("token exchange: status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out oauthTokenResponse
	decodeJSONBody(t, rr, &out)
	if out.AccessToken == "" || out.RefreshToken == "" {
		t.Fatalf("token exchange returned empty tokens: %s", rr.Body.String())
	}
	return out
}

func postForm(t *testing.T, srv *server.Server, path string, form url.Values, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if bearer != "" {
		if strings.HasPrefix(bearer, "stella_") {
			req.Header.Set("Authorization", "Bearer "+bearer)
		} else {
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: bearer})
		}
	}
	rr := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rr, req)
	return rr
}

func decodeJSONBody(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(rr.Body.Bytes()))
	if err := dec.Decode(dst); err != nil {
		t.Fatalf("decode JSON body %q: %v", rr.Body.String(), err)
	}
}
