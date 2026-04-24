package oauth

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// FlowBroker is the common interface for both device-code and authorization-code
// OAuth flows.
type FlowBroker interface {
	StartFlow(ctx context.Context, provider Provider, userID int64) (FlowStatus, error)
	Poll(ctx context.Context, flowID string) (FlowStatus, error)
}

// DeviceCodeBroker implements the OAuth2 device-code flow.
// It spawns a background goroutine that polls the token endpoint until the user
// authorizes the device.
type DeviceCodeBroker struct {
	cfg    *oauth2.Config
	store  *FlowStore
	mu     sync.Mutex
	secret map[string]*deviceFlowSecret // flowID → in-flight state
}

type deviceFlowSecret struct {
	cancel context.CancelFunc
	token  *oauth2.Token
	err    error
	done   chan struct{}
}

// NewDeviceCodeBroker creates a DeviceCodeBroker backed by store.
// cfg.Endpoint must have DeviceAuthEndpoint set.
func NewDeviceCodeBroker(cfg *oauth2.Config, store *FlowStore) *DeviceCodeBroker {
	return &DeviceCodeBroker{
		cfg:    cfg,
		store:  store,
		secret: make(map[string]*deviceFlowSecret),
	}
}

// StartFlow requests a device code, stores pending state, and returns the
// FlowStatus the caller should display. A background goroutine polls the
// token endpoint until the user authorizes or the flow expires.
func (b *DeviceCodeBroker) StartFlow(ctx context.Context, provider Provider, userID int64) (FlowStatus, error) {
	da, err := b.cfg.DeviceAuth(ctx, oauth2.AccessTypeOnline)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauth: device auth: %w", err)
	}

	flowID := uuid.NewString()
	expiresAt := da.Expiry

	status := FlowStatus{
		Provider:        provider,
		FlowID:          flowID,
		UserID:          userID,
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
	sec := &deviceFlowSecret{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	b.mu.Lock()
	b.secret[flowID] = sec
	b.mu.Unlock()

	go func() {
		defer close(sec.done)
		tok, err := b.cfg.DeviceAccessToken(bgCtx, da)
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
func (b *DeviceCodeBroker) Poll(ctx context.Context, flowID string) (FlowStatus, error) {
	status, ok := b.store.Get(flowID)
	if !ok {
		return FlowStatus{}, fmt.Errorf("oauth: unknown flow %q", flowID)
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

// Complete returns the token once the background goroutine finishes.
// Must be called only after Poll returns FlowStateAuthorized.
func (b *DeviceCodeBroker) Complete(ctx context.Context, flowID string) (*oauth2.Token, error) {
	b.mu.Lock()
	sec, ok := b.secret[flowID]
	b.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("oauth: no authorized token for flow %q", flowID)
	}

	<-sec.done

	b.mu.Lock()
	tok := sec.token
	err := sec.err
	b.mu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("oauth: flow %q failed: %w", flowID, err)
	}
	if tok == nil {
		return nil, fmt.Errorf("oauth: flow %q is not yet authorized", flowID)
	}

	b.store.Delete(flowID)
	b.cleanSecret(flowID)
	return tok, nil
}

func (b *DeviceCodeBroker) cleanSecret(flowID string) {
	b.mu.Lock()
	if sec, ok := b.secret[flowID]; ok {
		sec.cancel()
		delete(b.secret, flowID)
	}
	b.mu.Unlock()
}

// AuthCodeBroker implements the OAuth2 authorization-code flow.
// The user visits VerificationURI (an auth-code URL) and the callback handler
// calls Complete with the code returned by the provider.
type AuthCodeBroker struct {
	cfg   *oauth2.Config
	store *FlowStore
}

// NewAuthCodeBroker creates an AuthCodeBroker backed by store.
func NewAuthCodeBroker(cfg *oauth2.Config, store *FlowStore) *AuthCodeBroker {
	return &AuthCodeBroker{cfg: cfg, store: store}
}

// StartFlow generates a state token, constructs the authorization URL, and
// stores a pending FlowStatus. The user must navigate to VerificationURI.
func (b *AuthCodeBroker) StartFlow(ctx context.Context, provider Provider, userID int64) (FlowStatus, error) {
	flowID := uuid.NewString()
	status := FlowStatus{
		Provider:        provider,
		FlowID:          flowID,
		UserID:          userID,
		VerificationURI: b.cfg.AuthCodeURL(flowID),
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		State:           FlowStatePending,
	}
	b.store.Create(status)
	return status, nil
}

// Poll checks whether the flow has been completed externally.
func (b *AuthCodeBroker) Poll(ctx context.Context, flowID string) (FlowStatus, error) {
	status, ok := b.store.Get(flowID)
	if !ok {
		return FlowStatus{}, fmt.Errorf("oauth: unknown flow %q", flowID)
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

// Complete exchanges an authorization code for tokens and returns the token.
// The code comes from the OAuth callback handler's query parameter.
func (b *AuthCodeBroker) Complete(ctx context.Context, flowID string, code string) (*oauth2.Token, error) {
	if _, ok := b.store.Get(flowID); !ok {
		return nil, fmt.Errorf("oauth: unknown flow %q", flowID)
	}

	tok, err := b.cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("oauth: exchange code: %w", err)
	}

	b.store.Update(flowID, FlowStateAuthorized, nil)
	return tok, nil
}
