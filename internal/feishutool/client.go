package feishutool

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"golang.org/x/time/rate"
)

const defaultRateLimit = 50 // requests per second

// NeedAuthError is returned when a user access token is required but not available.
// The channel handler should catch this and prompt the user to authorize.
type NeedAuthError struct {
	OpenID string
}

func (e *NeedAuthError) Error() string {
	return fmt.Sprintf("feishutool: user %s needs to authorize via OAuth", e.OpenID)
}

// Client wraps a Lark SDK client with rate limiting and UAT support.
type Client struct {
	lark       *lark.Client
	limiter    *rate.Limiter
	tokenStore TokenStore
	appID      string
	appSecret  string
}

// ClientOption configures the Client.
type ClientOption func(*Client)

// WithRateLimit sets the per-second rate limit for API calls.
func WithRateLimit(rps int) ClientOption {
	return func(c *Client) {
		c.limiter = rate.NewLimiter(rate.Limit(rps), rps)
	}
}

// WithTokenStore sets the token store for UAT resolution.
func WithTokenStore(ts TokenStore) ClientOption {
	return func(c *Client) {
		c.tokenStore = ts
	}
}

// NewClient creates a feishutool Client wrapping the given Lark SDK client.
func NewClient(larkClient *lark.Client, opts ...ClientOption) *Client {
	c := &Client{
		lark:    larkClient,
		limiter: rate.NewLimiter(rate.Limit(defaultRateLimit), defaultRateLimit),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SetAppCredentials stores the app credentials needed for token refresh.
func (c *Client) SetAppCredentials(appID, appSecret string) {
	c.appID = appID
	c.appSecret = appSecret
}

// Lark returns the underlying Lark SDK client for direct API access.
func (c *Client) Lark() *lark.Client {
	return c.lark
}

// TokenStore returns the configured token store, or nil if none is set.
func (c *Client) TokenStore() TokenStore {
	return c.tokenStore
}

// Wait blocks until the rate limiter allows one request.
// Returns an error if the context is cancelled while waiting.
func (c *Client) Wait(ctx context.Context) error {
	if err := c.limiter.Wait(ctx); err != nil {
		return fmt.Errorf("feishutool: rate limit wait: %w", err)
	}
	return nil
}

// InvokeAsUser resolves the stored UAT for the user identified by open_id
// in the context, auto-refreshes if expired, and calls fn with the user
// access token. Falls back to bot token if no UAT is stored.
//
// If requireAuth is true and no token exists, returns NeedAuthError instead
// of falling back to bot token.
func (c *Client) InvokeAsUser(ctx context.Context, requireAuth bool, fn func(ctx context.Context, token string) error) error {
	if err := c.Wait(ctx); err != nil {
		return err
	}

	openID := OpenIDFromContext(ctx)
	if openID == "" || c.tokenStore == nil {
		if requireAuth {
			return &NeedAuthError{OpenID: openID}
		}
		// No UAT possible — fall back to bot token (caller uses c.Lark() directly).
		return fn(ctx, "")
	}

	token, err := c.tokenStore.Get(ctx, openID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// No stored token — fall back to bot token or require auth.
			if requireAuth {
				return &NeedAuthError{OpenID: openID}
			}
			return fn(ctx, "")
		}
		// Actual error (DB failure, decryption error) — surface it
		// instead of silently falling back to bot token.
		return fmt.Errorf("feishutool: get token for %s: %w", openID, err)
	}

	// Auto-refresh if access token expired but refresh token is still valid.
	if token.IsExpired() {
		if token.IsRefreshExpired() {
			// Both tokens expired — need re-authorization.
			_ = c.tokenStore.Delete(ctx, openID)
			if requireAuth {
				return &NeedAuthError{OpenID: openID}
			}
			return fn(ctx, "")
		}

		refreshed, err := c.refreshToken(ctx, token.RefreshToken)
		if err != nil {
			slog.Warn("feishutool: token refresh failed, falling back",
				"open_id", openID, "error", err)
			if requireAuth {
				return &NeedAuthError{OpenID: openID}
			}
			return fn(ctx, "")
		}

		if err := c.tokenStore.Set(ctx, openID, refreshed); err != nil {
			slog.Warn("feishutool: failed to store refreshed token",
				"open_id", openID, "error", err)
		}
		token = refreshed
	}

	return fn(ctx, token.AccessToken)
}

// refreshToken exchanges a refresh token for a new token pair via the Feishu
// OIDC refresh endpoint.
func (c *Client) refreshToken(ctx context.Context, refreshToken string) (Token, error) {
	appToken, err := c.getAppAccessToken(ctx)
	if err != nil {
		return Token{}, fmt.Errorf("get app access token: %w", err)
	}

	bodyMap := map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	body := string(bodyBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/authen/v1/oidc/refresh_access_token",
		strings.NewReader(body))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+appToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("refresh token: HTTP %d", resp.StatusCode)
	}
	return parseOIDCTokenResponse(resp.Body)
}

// getAppAccessToken retrieves the app access token (tenant token) via the
// internal Lark SDK API. This is needed for OIDC token operations.
func (c *Client) getAppAccessToken(ctx context.Context) (string, error) {
	bodyMap := map[string]string{
		"app_id":     c.appID,
		"app_secret": c.appSecret,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	body := string(bodyBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/auth/v3/app_access_token/internal",
		strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("app access token: HTTP %d", resp.StatusCode)
	}

	var result struct {
		Code           int    `json:"code"`
		Msg            string `json:"msg"`
		AppAccessToken string `json:"app_access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("app token error (code=%d): %s", result.Code, result.Msg)
	}
	return result.AppAccessToken, nil
}

// ExchangeCode exchanges an authorization code for user access tokens via
// the Feishu OIDC token endpoint.
func (c *Client) ExchangeCode(ctx context.Context, code string) (Token, error) {
	appToken, err := c.getAppAccessToken(ctx)
	if err != nil {
		return Token{}, fmt.Errorf("get app access token: %w", err)
	}

	bodyMap := map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	}
	bodyBytes, _ := json.Marshal(bodyMap)
	body := string(bodyBytes)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://open.feishu.cn/open-apis/authen/v1/oidc/access_token",
		strings.NewReader(body))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+appToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Token{}, fmt.Errorf("exchange code: HTTP %d", resp.StatusCode)
	}
	return parseOIDCTokenResponse(resp.Body)
}

// parseOIDCTokenResponse parses the common OIDC token response format used
// by both the access_token and refresh_access_token endpoints.
func parseOIDCTokenResponse(body io.Reader) (Token, error) {
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			ExpiresIn        int64  `json:"expires_in"`
			RefreshExpiresIn int64  `json:"refresh_expires_in"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&result); err != nil {
		return Token{}, fmt.Errorf("decode response: %w", err)
	}
	if result.Code != 0 {
		return Token{}, fmt.Errorf("OIDC error (code=%d): %s", result.Code, result.Msg)
	}

	now := time.Now()
	return Token{
		AccessToken:      result.Data.AccessToken,
		RefreshToken:     result.Data.RefreshToken,
		ExpiresAt:        now.Add(time.Duration(result.Data.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(result.Data.RefreshExpiresIn) * time.Second),
	}, nil
}

// GetTokenOwner calls the Feishu OIDC userinfo endpoint to retrieve the
// open_id of the user who owns the given access token. Used to verify that
// an exchanged token belongs to the expected user.
func (c *Client) GetTokenOwner(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://open.feishu.cn/open-apis/authen/v1/user_info", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			OpenID string `json:"open_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode userinfo: %w", err)
	}
	if result.Code != 0 {
		return "", fmt.Errorf("userinfo error (code=%d): %s", result.Code, result.Msg)
	}
	return result.Data.OpenID, nil
}

// AuthURL returns the Feishu OAuth authorization URL for the given app and redirect URI.
func AuthURL(appID, redirectURI, state string) string {
	params := url.Values{}
	params.Set("app_id", appID)
	if redirectURI != "" {
		params.Set("redirect_uri", redirectURI)
	}
	params.Set("state", state)
	return "https://open.feishu.cn/open-apis/authen/v1/authorize?" + params.Encode()
}

// InvokeWithUserToken calls fn with a resolved user access token.
// This is a convenience for SDK calls that accept larkcore.WithUserAccessToken.
func (c *Client) InvokeWithUserToken(ctx context.Context, requireAuth bool, fn func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error) error {
	return c.InvokeAsUser(ctx, requireAuth, func(ctx context.Context, token string) error {
		if token != "" {
			return fn(ctx, larkcore.WithUserAccessToken(token))
		}
		return fn(ctx)
	})
}
