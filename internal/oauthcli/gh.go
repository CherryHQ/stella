package oauthcli

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

const ghScope = "repo,read:org"

// GitHubConfig holds the OAuth app credentials for device flow.
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
}

// ghFlowSecret holds in-flight state for a GitHub device flow.
type ghFlowSecret struct {
	cancel context.CancelFunc
	token  *oauth2.Token
	err    error
	done   chan struct{}
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

func (b *GitHubBroker) oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID:     b.cfg.ClientID,
		ClientSecret: b.cfg.ClientSecret,
		Scopes:       []string{ghScope},
		Endpoint:     github.Endpoint,
	}
}

// StartDeviceFlow requests a device code from GitHub, stores pending state,
// and returns the FlowStatus the caller should display to the user.
// A background goroutine polls GitHub until the user authorizes or the flow expires.
func (b *GitHubBroker) StartDeviceFlow(ctx context.Context, userID int64) (FlowStatus, error) {
	cfg := b.oauthConfig()

	da, err := cfg.DeviceAuth(ctx, oauth2.AccessTypeOnline)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauthcli/gh: device auth: %w", err)
	}

	flowID := uuid.NewString()
	expiresAt := da.Expiry

	status := FlowStatus{
		Provider:        ProviderGitHub,
		FlowID:          flowID,
		VerificationURI: da.VerificationURIComplete,
		UserCode:        da.UserCode,
		ExpiresAt:       expiresAt,
		State:           FlowStatePending,
	}
	if status.VerificationURI == "" {
		status.VerificationURI = da.VerificationURI
	}

	b.store.Create(status)

	bgCtx, cancel := context.WithDeadline(context.Background(), expiresAt)
	sec := &ghFlowSecret{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	b.secret[flowID] = sec
	b.mu.Unlock()

	go func() {
		defer close(sec.done)
		tok, err := cfg.DeviceAccessToken(bgCtx, da)
		b.mu.Lock()
		sec.token = tok
		sec.err = err
		b.mu.Unlock()
		if err == nil {
			b.store.Update(flowID, FlowStateAuthorized, nil)
		} else {
			b.store.Update(flowID, FlowStateFailed, nil)
		}
	}()

	return status, nil
}

// Poll checks whether the user has completed authorization for flowID.
// It reads from the store — the background goroutine updates it when done.
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

	return status, nil
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

	// Wait for the background goroutine to finish if it hasn't yet.
	<-sec.done

	b.mu.Lock()
	tok := sec.token
	err := sec.err
	b.mu.Unlock()

	if err != nil {
		return fmt.Errorf("oauthcli/gh: flow %q failed: %w", flowID, err)
	}
	if tok == nil {
		return fmt.Errorf("oauthcli/gh: flow %q is not yet authorized", flowID)
	}

	bundle := GHOAuthBundle{
		Version:     1,
		AccessToken: tok.AccessToken,
		TokenType:   tok.TokenType,
	}
	if !tok.Expiry.IsZero() {
		bundle.ExpiresAt = &tok.Expiry
	}

	if err := SaveGHBundle(ctx, vs, userID, bundle); err != nil {
		return err
	}

	b.store.Delete(flowID)
	b.cleanSecret(flowID)
	return nil
}

func (b *GitHubBroker) cleanSecret(flowID string) {
	b.mu.Lock()
	if sec, ok := b.secret[flowID]; ok {
		sec.cancel()
		delete(b.secret, flowID)
	}
	b.mu.Unlock()
}
