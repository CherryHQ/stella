package oauth

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"golang.org/x/oauth2"
)

// FlowBroker is the common interface for both device-code and authorization-code
// OAuth flows.
type FlowBroker interface {
	StartFlow(ctx context.Context, provider Provider, userID string) (FlowStatus, error)
	Poll(ctx context.Context, flowID string) (FlowStatus, error)
}

// DeviceCodeBroker implements the OAuth2 device-code flow.
// It spawns a background goroutine that polls the token endpoint until the user
// authorizes the device.
type DeviceCodeBroker struct {
	cfg   *oauth2.Config
	store *FlowStore
	// onAuthorized persists the token once the user authorizes. It runs before
	// the flow is marked authorized so any observer (CLI status, Web UI poll)
	// sees a fully persisted connection. A nil callback skips persistence.
	onAuthorized func(flowID string, tok *oauth2.Token) error
}

// NewDeviceCodeBroker creates a DeviceCodeBroker backed by store. onAuthorized
// is invoked from the background goroutine to persist the token when the user
// authorizes; pass nil to skip persistence.
// cfg.Endpoint must have DeviceAuthEndpoint set.
func NewDeviceCodeBroker(cfg *oauth2.Config, store *FlowStore, onAuthorized func(flowID string, tok *oauth2.Token) error) *DeviceCodeBroker {
	return &DeviceCodeBroker{cfg: cfg, store: store, onAuthorized: onAuthorized}
}

// StartFlow requests a device code, stores pending state, and returns the
// FlowStatus the caller should display. A background goroutine polls the
// token endpoint until the user authorizes or the flow expires.
func (b *DeviceCodeBroker) StartFlow(ctx context.Context, provider Provider, userID string) (FlowStatus, error) {
	// RFC 8628 public-client device auth omits client_secret, but some providers
	// (e.g. Feishu/Lark) require it. Inject it when present so the device auth
	// request is accepted without changing the token exchange path.
	var extraOpts []oauth2.AuthCodeOption
	if b.cfg.ClientSecret != "" {
		extraOpts = append(extraOpts, oauth2.SetAuthURLParam("client_secret", b.cfg.ClientSecret))
	}
	da, err := b.cfg.DeviceAuth(ctx, extraOpts...)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("oauth: device auth: %w", err)
	}

	flowID := uuid.Must(uuid.NewV7()).String()
	expiresAt := da.Expiry

	status := FlowStatus{
		Provider:        provider,
		FlowID:          flowID,
		UserID:          userID,
		VerificationURI: da.VerificationURIComplete,
		UserCode:        da.UserCode,
		ExpiresAt:       expiresAt,
		State:           FlowStatePending,
		FlowType:        "device_code",
	}
	if status.VerificationURI == "" {
		status.VerificationURI = da.VerificationURI
	}

	b.store.Create(status)

	bgCtx, cancel := context.WithDeadline(context.Background(), expiresAt)
	go func() {
		defer cancel()
		tok, err := b.cfg.DeviceAccessToken(bgCtx, da)
		if err != nil {
			slog.Warn("oauth: device flow poll failed",
				"provider", provider, "user_id", userID, "flow_id", flowID, "error", err)
			b.store.Update(flowID, FlowStateFailed, func(fs *FlowStatus) { fs.Error = err.Error() })
			return
		}
		// Persist before marking authorized so the token is in the vault the
		// moment any observer sees the flow complete.
		if b.onAuthorized != nil {
			if err := b.onAuthorized(flowID, tok); err != nil {
				slog.Warn("oauth: device flow persist failed",
					"provider", provider, "user_id", userID, "flow_id", flowID, "error", err)
				b.store.Update(flowID, FlowStateFailed, func(fs *FlowStatus) { fs.Error = err.Error() })
				return
			}
		}
		b.store.Update(flowID, FlowStateAuthorized, func(fs *FlowStatus) { fs.Token = tok })
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
		status.State = FlowStateExpired
		return status, nil
	}

	return status, nil
}

// AuthCodeBroker implements the OAuth2 authorization-code flow.
// The user visits VerificationURI (an auth-code URL) and the callback handler
// calls Complete with the code returned by the provider.
type AuthCodeBroker struct {
	cfg   *oauth2.Config
	store *FlowStore
	pkce  bool
}

// NewAuthCodeBroker creates an AuthCodeBroker backed by store.
// When pkce is true, PKCE (RFC 7636) is enabled: a verifier is generated per flow
// and the S256 challenge is included in the authorization URL.
func NewAuthCodeBroker(cfg *oauth2.Config, store *FlowStore, pkce bool) *AuthCodeBroker {
	return &AuthCodeBroker{cfg: cfg, store: store, pkce: pkce}
}

// StartFlow generates a state token, constructs the authorization URL, and
// stores a pending FlowStatus. The user must navigate to VerificationURI.
func (b *AuthCodeBroker) StartFlow(ctx context.Context, provider Provider, userID string) (FlowStatus, error) {
	flowID := uuid.Must(uuid.NewV7()).String()
	var verifier string
	var authURLOpts []oauth2.AuthCodeOption
	if b.pkce {
		verifier = oauth2.GenerateVerifier()
		authURLOpts = append(authURLOpts, oauth2.S256ChallengeOption(verifier))
	}
	status := FlowStatus{
		Provider:        provider,
		FlowID:          flowID,
		UserID:          userID,
		VerificationURI: b.cfg.AuthCodeURL(flowID, authURLOpts...),
		ExpiresAt:       time.Now().Add(10 * time.Minute),
		State:           FlowStatePending,
		FlowType:        "authorization_code",
		PKCEVerifier:    verifier,
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
	flow, ok := b.store.Get(flowID)
	if !ok {
		return nil, fmt.Errorf("oauth: unknown flow %q", flowID)
	}

	var opts []oauth2.AuthCodeOption
	if flow.PKCEVerifier != "" {
		opts = append(opts, oauth2.VerifierOption(flow.PKCEVerifier))
	}
	tok, err := b.cfg.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, fmt.Errorf("oauth: exchange code: %w", err)
	}

	b.store.Update(flowID, FlowStateAuthorized, nil)
	return tok, nil
}
