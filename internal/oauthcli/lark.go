package oauthcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

const (
	larkBaseFeishu = "https://open.feishu.cn"
	larkBaseLark   = "https://open.larksuite.com"

	larkRedirectURI = "https://anna.app/oauth/lark/callback" // placeholder; caller sets up the real redirect
	larkScope       = "contact:user.base:readonly"
)

// LarkConfig holds the OAuth app credentials for Lark/Feishu device-style flow.
type LarkConfig struct {
	AppID     string
	AppSecret string
	Brand     string // "lark" or "feishu"
}

// LarkBroker manages Lark/Feishu OAuth sessions via the authorization-code
// flow adapted for device-like use: StartDeviceFlow returns a URL the user
// visits; Poll checks completion; Complete exchanges the code and saves the
// bundle to vault.
type LarkBroker struct {
	cfg         LarkConfig
	store       *FlowStore
	redirectURI string
}

// NewLarkBroker constructs a LarkBroker. redirectURI is the OAuth callback URL
// that your HTTP handler will receive and then call Complete on.
func NewLarkBroker(cfg LarkConfig, store *FlowStore) *LarkBroker {
	return &LarkBroker{cfg: cfg, store: store, redirectURI: larkRedirectURI}
}

// WithRedirectURI returns a new broker with the same configuration but the
// given redirect URI.
func (b *LarkBroker) WithRedirectURI(uri string) *LarkBroker {
	return &LarkBroker{cfg: b.cfg, store: b.store, redirectURI: uri}
}

func larkBaseURL(brand string) string {
	if brand == "feishu" {
		return larkBaseFeishu
	}
	return larkBaseLark
}

func (b *LarkBroker) oauthConfig() *oauth2.Config {
	base := larkBaseURL(b.cfg.Brand)
	return &oauth2.Config{
		ClientID:    b.cfg.AppID,
		RedirectURL: b.redirectURI,
		Scopes:      []string{larkScope},
		Endpoint: oauth2.Endpoint{
			AuthURL:   base + "/open-apis/authen/v1/authorize",
			TokenURL:  base + "/open-apis/authen/v1/oidc/access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

// larkTokenTransport adapts Lark's non-standard OIDC token endpoints for use
// with golang.org/x/oauth2. It:
//   - converts the library's form-encoded request body to JSON (Lark expects JSON),
//   - injects the app access token as Authorization: Bearer,
//   - unwraps Lark's {"code":0,"data":{…}} envelope to flat OAuth2 JSON so the
//     library can parse the response normally.
//
// refresh_expires_in is preserved as an extra field so callers can read it via
// tok.Extra("refresh_expires_in").
type larkTokenTransport struct {
	appToken string
}

func (t *larkTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.appToken)

	// oauth2 sends form-encoded; Lark's OIDC endpoints expect JSON.
	if req.Body != nil && req.Header.Get("Content-Type") == "application/x-www-form-urlencoded" {
		raw, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		vals, _ := url.ParseQuery(string(raw))
		m := make(map[string]string, len(vals))
		for k, vs := range vals {
			if len(vs) > 0 {
				m[k] = vs[0]
			}
		}
		jsonBody, _ := json.Marshal(m)
		req.Body = io.NopCloser(bytes.NewReader(jsonBody))
		req.ContentLength = int64(len(jsonBody))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	var larkResp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			ExpiresIn        int    `json:"expires_in"`
			RefreshExpiresIn int    `json:"refresh_expires_in"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &larkResp); err != nil {
		// Not a Lark envelope — pass through as-is.
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}
	if larkResp.Code != 0 {
		errBody := fmt.Appendf(nil, `{"error":"lark_%d","error_description":%q}`, larkResp.Code, larkResp.Msg)
		resp.StatusCode = http.StatusBadRequest
		resp.Body = io.NopCloser(bytes.NewReader(errBody))
		resp.ContentLength = int64(len(errBody))
		return resp, nil
	}

	// Rewrite to flat OAuth2 JSON; include refresh_expires_in as an extra field
	// so callers can read it via tok.Extra("refresh_expires_in").
	flat := struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		TokenType        string `json:"token_type"`
		RefreshExpiresIn int    `json:"refresh_expires_in"`
	}{
		AccessToken:      larkResp.Data.AccessToken,
		RefreshToken:     larkResp.Data.RefreshToken,
		ExpiresIn:        larkResp.Data.ExpiresIn,
		TokenType:        "Bearer",
		RefreshExpiresIn: larkResp.Data.RefreshExpiresIn,
	}
	flatBody, _ := json.Marshal(flat)
	resp.StatusCode = http.StatusOK
	resp.Body = io.NopCloser(bytes.NewReader(flatBody))
	resp.ContentLength = int64(len(flatBody))
	return resp, nil
}

// larkOAuthContext returns ctx with an HTTP client that routes Lark token
// requests through larkTokenTransport.
func larkOAuthContext(ctx context.Context, appToken string) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, &http.Client{
		Transport: &larkTokenTransport{appToken: appToken},
	})
}

// StartDeviceFlow generates a state token, constructs the authorization URL,
// and stores a pending FlowStatus. The user must navigate to VerificationURI.
func (b *LarkBroker) StartDeviceFlow(ctx context.Context, userID int64) (FlowStatus, error) {
	flowID := uuid.NewString()
	status := FlowStatus{
		Provider:        ProviderLark,
		FlowID:          flowID,
		VerificationURI: b.oauthConfig().AuthCodeURL(flowID),
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		State:           FlowStatePending,
	}
	b.store.Create(status)
	return status, nil
}

// Poll checks whether the flow identified by flowID has been completed.
// Completion is signaled externally via Complete after the OAuth callback is received.
func (b *LarkBroker) Poll(ctx context.Context, flowID string) (FlowStatus, error) {
	status, ok := b.store.Get(flowID)
	if !ok {
		return FlowStatus{}, fmt.Errorf("oauthcli/lark: unknown flow %q", flowID)
	}
	if status.State != FlowStatePending {
		return status, nil
	}
	if time.Now().After(status.ExpiresAt) {
		b.store.Update(flowID, FlowStateExpired, nil)
		status.State = FlowStateExpired
	}
	return status, nil
}

// Complete exchanges an authorization code for tokens, saves the bundle to
// vault, and marks the flow as authorized. The code comes from your OAuth
// callback handler's query parameter.
func (b *LarkBroker) Complete(ctx context.Context, vs VaultStore, userID int64, flowID string, code string) error {
	if _, ok := b.store.Get(flowID); !ok {
		return fmt.Errorf("oauthcli/lark: unknown flow %q", flowID)
	}

	appToken, err := fetchLarkAppToken(ctx, larkBaseURL(b.cfg.Brand), b.cfg.AppID, b.cfg.AppSecret)
	if err != nil {
		return fmt.Errorf("oauthcli/lark: fetch app access token: %w", err)
	}

	tok, err := b.oauthConfig().Exchange(larkOAuthContext(ctx, appToken), code)
	if err != nil {
		return fmt.Errorf("oauthcli/lark: exchange code: %w", err)
	}

	bundle := larkBundleFromToken(tok, b.cfg.AppID, b.cfg.AppSecret, b.cfg.Brand)
	if err := SaveLarkBundle(ctx, vs, userID, bundle); err != nil {
		return err
	}

	b.store.Update(flowID, FlowStateAuthorized, nil)
	b.store.Delete(flowID)
	return nil
}

// larkAppTokenResponse is the JSON body from the app_access_token endpoint.
type larkAppTokenResponse struct {
	Code           int    `json:"code"`
	Msg            string `json:"msg"`
	AppAccessToken string `json:"app_access_token"`
	Expire         int    `json:"expire"`
}

// fetchLarkAppToken obtains a short-lived Lark app access token.
// Lark requires a separate server-to-server credential exchange before any
// user token operation — this step has no equivalent in standard OAuth2.
func fetchLarkAppToken(ctx context.Context, base, appID, appSecret string) (string, error) {
	endpoint := base + "/open-apis/auth/v3/app_access_token/internal"
	var resp larkAppTokenResponse
	if err := postJSON(ctx, endpoint, map[string]string{"app_id": appID, "app_secret": appSecret}, nil, &resp); err != nil {
		return "", fmt.Errorf("app_access_token request: %w", err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("app_access_token error %d: %s", resp.Code, resp.Msg)
	}
	return resp.AppAccessToken, nil
}

// larkBundleFromToken builds a LarkOAuthBundle from an oauth2.Token returned
// by Exchange or TokenSource. The non-standard refresh_expires_in field is
// read from the token's extra claims.
func larkBundleFromToken(tok *oauth2.Token, appID, appSecret, brand string) LarkOAuthBundle {
	bundle := LarkOAuthBundle{
		Version:         1,
		AppID:           appID,
		AppSecret:       appSecret,
		Brand:           brand,
		AccessToken:     tok.AccessToken,
		RefreshToken:    tok.RefreshToken,
		AccessExpiresAt: tok.Expiry,
	}
	if ri, ok := tok.Extra("refresh_expires_in").(float64); ok && ri > 0 {
		bundle.RefreshExpiresAt = time.Now().Add(time.Duration(ri) * time.Second)
	}
	return bundle
}
