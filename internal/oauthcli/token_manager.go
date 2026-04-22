package oauthcli

import (
	"context"
	"fmt"
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
//
// Current lark-cli releases read the LARKSUITE_CLI_* variables for configless
// runtime auth.
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
		"LARKSUITE_CLI_USER_ACCESS_TOKEN": bundle.AccessToken,
		"LARKSUITE_CLI_APP_ID":            bundle.AppID,
		"LARKSUITE_CLI_BRAND":             bundle.Brand,
	}, nil
}

// refreshLarkToken uses the oauth2 library's TokenSource to refresh the user
// access token against Lark's v2 token endpoint (standard OAuth2 form-encoded,
// no app access token pre-fetch required).
func (m *TokenManager) refreshLarkToken(ctx context.Context, bundle *LarkOAuthBundle) (*LarkOAuthBundle, error) {
	cfg := &oauth2.Config{
		ClientID:     bundle.AppID,
		ClientSecret: bundle.AppSecret,
		Endpoint: oauth2.Endpoint{
			TokenURL:  larkBaseURL(bundle.Brand) + "/open-apis/authen/v2/oauth/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
	existing := &oauth2.Token{
		AccessToken:  bundle.AccessToken,
		RefreshToken: bundle.RefreshToken,
		Expiry:       bundle.AccessExpiresAt, // expired → triggers refresh
	}
	tok, err := cfg.TokenSource(ctx, existing).Token()
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	refreshed := larkBundleFromToken(tok, bundle.AppID, bundle.AppSecret, bundle.Brand)
	refreshed.Version = bundle.Version
	if refreshed.RefreshExpiresAt.IsZero() {
		refreshed.RefreshExpiresAt = bundle.RefreshExpiresAt
	}
	return &refreshed, nil
}
