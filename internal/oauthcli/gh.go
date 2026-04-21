package oauthcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	ghDeviceCodeURL  = "https://github.com/login/device/code"
	ghAccessTokenURL = "https://github.com/login/oauth/access_token"
	ghScope          = "repo,read:org"
)

// GitHubConfig holds the OAuth app credentials for device flow.
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
}

// ghFlowSecret holds provider-specific secrets for an in-flight GitHub flow.
type ghFlowSecret struct {
	deviceCode string
	interval   int // seconds between polls, as returned by GitHub
	bundle     *GHOAuthBundle
}

// GitHubBroker manages GitHub device-flow sessions.
type GitHubBroker struct {
	cfg    GitHubConfig
	store  *FlowStore
	mu     sync.Mutex
	secret map[string]*ghFlowSecret // flowID → secrets
}

// NewGitHubBroker constructs a GitHubBroker.
func NewGitHubBroker(cfg GitHubConfig, store *FlowStore) *GitHubBroker {
	return &GitHubBroker{
		cfg:    cfg,
		store:  store,
		secret: make(map[string]*ghFlowSecret),
	}
}

// ghDeviceCodeResponse is the JSON body from POST /login/device/code.
type ghDeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// ghAccessTokenResponse is the JSON body from POST /login/oauth/access_token.
type ghAccessTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Error       string `json:"error"`
	// Fine-grained tokens may carry expiry.
	ExpiresIn    int    `json:"expires_in,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

// StartDeviceFlow requests a device code from GitHub, stores pending state,
// and returns the FlowStatus the caller should display to the user.
func (b *GitHubBroker) StartDeviceFlow(ctx context.Context, userID int64) (FlowStatus, error) {
	body := url.Values{}
	body.Set("client_id", b.cfg.ClientID)
	body.Set("scope", ghScope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ghDeviceCodeURL,
		strings.NewReader(body.Encode()))
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: build device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: device code request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: device code: unexpected status %d", resp.StatusCode)
	}

	var dc ghDeviceCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: decode device code response: %w", err)
	}

	if dc.Interval <= 0 {
		dc.Interval = 5 // GitHub default
	}

	flowID := uuid.NewString()
	expiresAt := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	status := FlowStatus{
		Provider:        ProviderGitHub,
		FlowID:          flowID,
		VerificationURI: dc.VerificationURI,
		UserCode:        dc.UserCode,
		ExpiresAt:       expiresAt,
		State:           FlowStatePending,
	}

	b.store.Create(status)

	b.mu.Lock()
	b.secret[flowID] = &ghFlowSecret{
		deviceCode: dc.DeviceCode,
		interval:   dc.Interval,
	}
	b.mu.Unlock()

	return status, nil
}

// Poll checks whether the user has completed authorization for flowID.
// It updates the store state and returns the current FlowStatus.
// Callers must call Complete after Poll returns State == FlowStateAuthorized.
func (b *GitHubBroker) Poll(ctx context.Context, flowID string) (FlowStatus, error) {
	status, ok := b.store.Get(flowID)
	if !ok {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: unknown flow %q", flowID)
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
	if !ok {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: missing secrets for flow %q", flowID)
	}

	reqBody := url.Values{}
	reqBody.Set("client_id", b.cfg.ClientID)
	reqBody.Set("device_code", sec.deviceCode)
	reqBody.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ghAccessTokenURL,
		strings.NewReader(reqBody.Encode()))
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: build poll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: poll request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var at ghAccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&at); err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: decode poll response: %w", err)
	}

	switch at.Error {
	case "":
		// Success — stash the token bundle, mark authorized.
		bundle := &GHOAuthBundle{
			Version:      1,
			AccessToken:  at.AccessToken,
			TokenType:    at.TokenType,
			Scope:        at.Scope,
			RefreshToken: at.RefreshToken,
		}
		if at.ExpiresIn > 0 {
			t := time.Now().Add(time.Duration(at.ExpiresIn) * time.Second)
			bundle.ExpiresAt = &t
		}
		b.mu.Lock()
		sec.bundle = bundle
		b.mu.Unlock()
		b.store.Update(flowID, FlowStateAuthorized, nil)
		status.State = FlowStateAuthorized
		return status, nil

	case "authorization_pending":
		// Still waiting — no change.
		return status, nil

	case "slow_down":
		// GitHub wants us to back off; bump the stored interval and keep waiting.
		b.mu.Lock()
		sec.interval += 5
		b.mu.Unlock()
		return status, nil

	case "expired_token":
		b.store.Update(flowID, FlowStateExpired, nil)
		b.cleanSecret(flowID)
		status.State = FlowStateExpired
		return status, nil

	case "access_denied":
		b.store.Update(flowID, FlowStateFailed, nil)
		b.cleanSecret(flowID)
		status.State = FlowStateFailed
		return status, fmt.Errorf("oauthcli/gh: access denied by user")

	default:
		b.store.Update(flowID, FlowStateFailed, nil)
		b.cleanSecret(flowID)
		status.State = FlowStateFailed
		return status, fmt.Errorf("oauthcli/gh: unexpected error %q from GitHub", at.Error)
	}
}

// Complete persists the token bundle to vault. Must be called only after Poll
// returns State == FlowStateAuthorized.
func (b *GitHubBroker) Complete(ctx context.Context, vs VaultStore, userID int64, flowID string) error {
	b.mu.Lock()
	sec, ok := b.secret[flowID]
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("oauthcli/gh: no authorized token for flow %q", flowID)
	}
	if sec.bundle == nil {
		return fmt.Errorf("oauthcli/gh: flow %q is not yet authorized", flowID)
	}

	if err := SaveGHBundle(ctx, vs, userID, *sec.bundle); err != nil {
		return err
	}

	b.store.Delete(flowID)
	b.cleanSecret(flowID)
	return nil
}

// PollInterval returns the current recommended seconds-between-polls for a
// flow. Returns 5 (GitHub default) if the flow is unknown.
func (b *GitHubBroker) PollInterval(flowID string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sec, ok := b.secret[flowID]; ok {
		return sec.interval
	}
	return 5
}

func (b *GitHubBroker) cleanSecret(flowID string) {
	b.mu.Lock()
	delete(b.secret, flowID)
	b.mu.Unlock()
}

// postJSON is a small helper to POST a JSON body and decode the response.
func postJSON(ctx context.Context, url string, reqBody any, headers map[string]string, out any) error {
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
