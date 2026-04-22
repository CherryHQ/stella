package oauthcli

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
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

	return map[string]string{
		"LARK_ACCESS_TOKEN": bundle.AccessToken,
		"LARK_APP_ID":       bundle.AppID,
		"LARK_BRAND":        bundle.Brand,
	}, nil
}

// refreshLarkToken fetches a new app access token, then uses the oauth2
// library's TokenSource to refresh the user access token via Lark's OIDC
// refresh endpoint. larkTokenTransport adapts Lark's non-standard request
// and response format for the library.
func (m *TokenManager) refreshLarkToken(ctx context.Context, bundle *LarkOAuthBundle) (*LarkOAuthBundle, error) {
	base := larkBaseURL(bundle.Brand)

	appToken, err := fetchLarkAppToken(ctx, base, bundle.AppID, bundle.AppSecret)
	if err != nil {
		return nil, fmt.Errorf("fetch app access token: %w", err)
	}

	cfg := &oauth2.Config{
		ClientID: bundle.AppID,
		Endpoint: oauth2.Endpoint{
			// Lark uses a distinct endpoint for token refresh.
			TokenURL:  base + "/open-apis/authen/v1/oidc/refresh_access_token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	existing := &oauth2.Token{
		AccessToken:  bundle.AccessToken,
		RefreshToken: bundle.RefreshToken,
		Expiry:       bundle.AccessExpiresAt, // expired → triggers refresh
	}
	refreshCtx := context.WithValue(ctx, oauth2.HTTPClient, &http.Client{
		Transport: &larkTokenTransport{appToken: appToken},
	})
	tok, err := cfg.TokenSource(refreshCtx, existing).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	refreshed := larkBundleFromToken(tok, bundle.AppID, bundle.AppSecret, bundle.Brand)
	// larkBundleFromToken sets Version to 1; preserve the existing version.
	refreshed.Version = bundle.Version
	// Only update RefreshExpiresAt if the server returned a new value.
	if refreshed.RefreshExpiresAt.IsZero() {
		refreshed.RefreshExpiresAt = bundle.RefreshExpiresAt
	}
	return &refreshed, nil
}
