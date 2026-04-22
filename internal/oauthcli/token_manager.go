package oauthcli

import (
	"context"
	"fmt"
	"time"
)

// tokenExpirySafetyMargin is subtracted from expiry times to avoid using
// tokens that are about to expire during a long-running operation.
const tokenExpirySafetyMargin = 2 * time.Minute

// TokenManager provides host-side token validation and (for Lark) automatic
// refresh. It reads from and writes to the vault.
type TokenManager struct {
	vs VaultStore
}

// NewTokenManager constructs a TokenManager backed by vs.
func NewTokenManager(vs VaultStore) *TokenManager {
	return &TokenManager{vs: vs}
}

// GetGHToken returns a valid GitHub access token for userID.
// GitHub tokens obtained via device flow do not expire in standard flow, so
// this simply returns whatever is in the vault bundle.
func (m *TokenManager) GetGHToken(ctx context.Context, userID int64) (string, error) {
	bundle, err := LoadGHBundle(ctx, m.vs, userID)
	if err != nil {
		return "", fmt.Errorf("oauthcli: get gh token: %w", err)
	}
	if bundle == nil {
		return "", fmt.Errorf("oauthcli: get gh token: user %d has not connected GitHub", userID)
	}
	if bundle.AccessToken == "" {
		return "", fmt.Errorf("oauthcli: get gh token: empty access token in vault for user %d", userID)
	}
	return bundle.AccessToken, nil
}

// GetLarkRuntimeEnv returns the environment variables needed for lark-cli.
// It loads the Lark bundle, refreshes the access token if expired, and returns
// a map ready for env injection. Returns an error if the bundle is absent or
// the refresh token has expired, so callers can skip injection without failing.
func (m *TokenManager) GetLarkRuntimeEnv(ctx context.Context, userID int64) (map[string]string, error) {
	bundle, err := LoadLarkBundle(ctx, m.vs, userID)
	if err != nil {
		return nil, fmt.Errorf("oauthcli: get lark env: %w", err)
	}
	if bundle == nil {
		return nil, fmt.Errorf("oauthcli: get lark env: user %d has not connected Lark/Feishu", userID)
	}

	now := time.Now()

	// Check refresh token expiry first — if this is gone there's nothing we can do.
	if now.After(bundle.RefreshExpiresAt.Add(-tokenExpirySafetyMargin)) {
		return nil, fmt.Errorf(
			"oauthcli: get lark env: user %d Lark refresh token expired at %s; please re-authorize",
			userID, bundle.RefreshExpiresAt.Format(time.RFC3339),
		)
	}

	// Refresh the access token if it is expired or about to expire.
	if now.After(bundle.AccessExpiresAt.Add(-tokenExpirySafetyMargin)) {
		refreshed, err := m.refreshLarkToken(ctx, bundle)
		if err != nil {
			return nil, fmt.Errorf("oauthcli: get lark env: refresh: %w", err)
		}
		if err := SaveLarkBundle(ctx, m.vs, userID, *refreshed); err != nil {
			return nil, fmt.Errorf("oauthcli: get lark env: save refreshed bundle: %w", err)
		}
		bundle = refreshed
	}

	env := map[string]string{
		"LARK_ACCESS_TOKEN": bundle.AccessToken,
		"LARK_APP_ID":       bundle.AppID,
		"LARK_BRAND":        bundle.Brand,
	}
	return env, nil
}

// larkRefreshResponse is the JSON body from the OIDC refresh_access_token endpoint.
type larkRefreshResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		RefreshExpiresIn int    `json:"refresh_expires_in"`
	} `json:"data"`
}

// refreshLarkToken exchanges the bundle's refresh token for a new access
// token, returning an updated bundle. The original bundle is not mutated.
func (m *TokenManager) refreshLarkToken(ctx context.Context, bundle *LarkOAuthBundle) (*LarkOAuthBundle, error) {
	base := larkBaseFeishu
	if bundle.Brand == "lark" {
		base = larkBaseLark
	}

	// Obtain a fresh app access token using the stored credentials.
	appTokenEndpoint := base + "/open-apis/auth/v3/app_access_token/internal"
	appTokenBody := map[string]string{
		"app_id":     bundle.AppID,
		"app_secret": bundle.AppSecret,
	}
	var appTokenResp larkAppTokenResponse
	if err := postJSON(ctx, appTokenEndpoint, appTokenBody, nil, &appTokenResp); err != nil {
		return nil, fmt.Errorf("fetch app access token: %w", err)
	}
	if appTokenResp.Code != 0 {
		return nil, fmt.Errorf("app_access_token error %d: %s", appTokenResp.Code, appTokenResp.Msg)
	}

	// Refresh the user access token.
	refreshEndpoint := base + "/open-apis/authen/v1/oidc/refresh_access_token"
	refreshBody := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": bundle.RefreshToken,
	}
	headers := map[string]string{
		"Authorization": "Bearer " + appTokenResp.AppAccessToken,
	}
	var refreshResp larkRefreshResponse
	if err := postJSON(ctx, refreshEndpoint, refreshBody, headers, &refreshResp); err != nil {
		return nil, fmt.Errorf("refresh token request: %w", err)
	}
	if refreshResp.Code != 0 {
		return nil, fmt.Errorf("refresh token error %d: %s", refreshResp.Code, refreshResp.Msg)
	}

	now := time.Now()
	refreshed := *bundle // shallow copy; all fields are value types or immutable strings
	refreshed.AccessToken = refreshResp.Data.AccessToken
	if refreshResp.Data.RefreshToken != "" {
		refreshed.RefreshToken = refreshResp.Data.RefreshToken
	}
	refreshed.AccessExpiresAt = now.Add(time.Duration(refreshResp.Data.ExpiresIn) * time.Second)
	if refreshResp.Data.RefreshExpiresIn > 0 {
		refreshed.RefreshExpiresAt = now.Add(time.Duration(refreshResp.Data.RefreshExpiresIn) * time.Second)
	}
	return &refreshed, nil
}
