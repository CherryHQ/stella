package oauthcli

import (
	"context"
	"fmt"
	"sync"
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

// larkFlowSecret holds provider-specific state for an in-flight Lark flow.
type larkFlowSecret struct {
	bundle *LarkOAuthBundle
}

// LarkBroker manages Lark/Feishu OAuth sessions via the authorization-code
// flow adapted for device-like use: StartDeviceFlow returns a URL the user
// visits; Poll checks completion; Complete exchanges the code and saves the
// bundle to vault.
type LarkBroker struct {
	cfg         LarkConfig
	store       *FlowStore
	redirectURI string
	mu          sync.Mutex
	secret      map[string]*larkFlowSecret
}

// NewLarkBroker constructs a LarkBroker. redirectURI is the OAuth callback URL
// that your HTTP handler will receive and then call Complete on.
func NewLarkBroker(cfg LarkConfig, store *FlowStore) *LarkBroker {
	return &LarkBroker{
		cfg:         cfg,
		store:       store,
		redirectURI: larkRedirectURI,
		secret:      make(map[string]*larkFlowSecret),
	}
}

// WithRedirectURI returns a new broker with the same configuration but the
// given redirect URI. The new broker has its own independent mutex and secret
// map, so it is safe to use concurrently with the original.
func (b *LarkBroker) WithRedirectURI(uri string) *LarkBroker {
	return &LarkBroker{
		cfg:         b.cfg,
		store:       b.store,
		redirectURI: uri,
		secret:      make(map[string]*larkFlowSecret),
	}
}

func (b *LarkBroker) baseURL() string {
	if b.cfg.Brand == "feishu" {
		return larkBaseFeishu
	}
	return larkBaseLark
}

func (b *LarkBroker) oauthConfig() *oauth2.Config {
	base := b.baseURL()
	return &oauth2.Config{
		ClientID:    b.cfg.AppID,
		RedirectURL: b.redirectURI,
		Scopes:      []string{larkScope},
		Endpoint: oauth2.Endpoint{
			AuthURL:  base + "/open-apis/authen/v1/authorize",
			TokenURL: base + "/open-apis/authen/v1/oidc/access_token",
		},
	}
}

// StartDeviceFlow generates a state token, constructs the authorization URL,
// and stores a pending FlowStatus. The user must navigate to VerificationURI.
func (b *LarkBroker) StartDeviceFlow(ctx context.Context, userID int64) (FlowStatus, error) {
	flowID := uuid.NewString()
	expiresAt := time.Now().Add(10 * time.Minute)

	authURL := b.oauthConfig().AuthCodeURL(flowID)

	status := FlowStatus{
		Provider:        ProviderLark,
		FlowID:          flowID,
		VerificationURI: authURL,
		ExpiresAt:       expiresAt,
		State:           FlowStatePending,
	}

	b.store.Create(status)
	b.mu.Lock()
	b.secret[flowID] = &larkFlowSecret{}
	b.mu.Unlock()

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
		b.cleanSecret(flowID)
		status.State = FlowStateExpired
		return status, nil
	}

	b.mu.Lock()
	sec, ok := b.secret[flowID]
	b.mu.Unlock()
	if ok && sec.bundle != nil {
		status.State = FlowStateAuthorized
		return status, nil
	}

	return status, nil
}

// Complete exchanges an authorization code for tokens, saves the bundle to
// vault, and marks the flow as authorized. The code comes from your OAuth
// callback handler's query parameter.
func (b *LarkBroker) Complete(ctx context.Context, vs VaultStore, userID int64, flowID string, code string) error {
	_, ok := b.store.Get(flowID)
	if !ok {
		return fmt.Errorf("oauthcli/lark: unknown flow %q", flowID)
	}

	appToken, err := b.fetchAppAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("oauthcli/lark: fetch app access token: %w", err)
	}

	bundle, err := b.exchangeCode(ctx, appToken, code)
	if err != nil {
		return fmt.Errorf("oauthcli/lark: exchange code: %w", err)
	}
	bundle.AppID = b.cfg.AppID
	bundle.AppSecret = b.cfg.AppSecret
	bundle.Brand = b.cfg.Brand
	bundle.Version = 1

	if err := SaveLarkBundle(ctx, vs, userID, *bundle); err != nil {
		return err
	}

	b.store.Update(flowID, FlowStateAuthorized, nil)
	b.mu.Lock()
	if sec, ok := b.secret[flowID]; ok {
		sec.bundle = bundle
	}
	b.mu.Unlock()

	b.store.Delete(flowID)
	b.cleanSecret(flowID)
	return nil
}

// larkAppTokenResponse is the JSON body from the app_access_token endpoint.
type larkAppTokenResponse struct {
	Code           int    `json:"code"`
	Msg            string `json:"msg"`
	AppAccessToken string `json:"app_access_token"`
	Expire         int    `json:"expire"`
}

// fetchAppAccessToken obtains a short-lived Lark app access token.
// Lark requires a separate app credential exchange before user token operations —
// the standard oauth2 flow does not support this pattern.
func (b *LarkBroker) fetchAppAccessToken(ctx context.Context) (string, error) {
	endpoint := b.baseURL() + "/open-apis/auth/v3/app_access_token/internal"
	reqBody := map[string]string{
		"app_id":     b.cfg.AppID,
		"app_secret": b.cfg.AppSecret,
	}
	var resp larkAppTokenResponse
	if err := postJSON(ctx, endpoint, reqBody, nil, &resp); err != nil {
		return "", fmt.Errorf("app_access_token request: %w", err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("app_access_token error %d: %s", resp.Code, resp.Msg)
	}
	return resp.AppAccessToken, nil
}

// larkUserTokenResponse is the JSON body from the OIDC access_token endpoint.
type larkUserTokenResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		RefreshExpiresIn int    `json:"refresh_expires_in"`
		TokenType        string `json:"token_type"`
	} `json:"data"`
}

// exchangeCode exchanges an authorization code for a user access token.
// Uses custom HTTP because Lark requires Authorization: Bearer <app_access_token>,
// which the standard oauth2.Config.Exchange() does not support.
func (b *LarkBroker) exchangeCode(ctx context.Context, appToken string, code string) (*LarkOAuthBundle, error) {
	endpoint := b.baseURL() + "/open-apis/authen/v1/oidc/access_token"
	reqBody := map[string]string{
		"grant_type": "authorization_code",
		"code":       code,
	}
	headers := map[string]string{
		"Authorization": "Bearer " + appToken,
	}
	var resp larkUserTokenResponse
	if err := postJSON(ctx, endpoint, reqBody, headers, &resp); err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("token exchange error %d: %s", resp.Code, resp.Msg)
	}

	now := time.Now()
	bundle := &LarkOAuthBundle{
		AccessToken:      resp.Data.AccessToken,
		RefreshToken:     resp.Data.RefreshToken,
		AccessExpiresAt:  now.Add(time.Duration(resp.Data.ExpiresIn) * time.Second),
		RefreshExpiresAt: now.Add(time.Duration(resp.Data.RefreshExpiresIn) * time.Second),
	}
	return bundle, nil
}

func (b *LarkBroker) cleanSecret(flowID string) {
	b.mu.Lock()
	delete(b.secret, flowID)
	b.mu.Unlock()
}
