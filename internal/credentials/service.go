package credentials

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"

	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/vault"
)

const (
	githubProviderID = "github"

	// ghOAuthClientID is GitHub CLI's public OAuth app client ID. GitHub's
	// device flow only requires the client ID, so Anna can offer gh OAuth
	// without any admin-side plugin configuration.
	ghOAuthClientID = "178c6fc778ccc68e1d6a"
)

// Service is the shared host-side credential manager. It owns vault secret
// operations and OAuth orchestration. Admin HTTP handlers and the built-in
// credentials tool both delegate to this service.
type Service struct {
	vaultSvc          *vault.Service
	pluginCfg         pluginhost.ConfigBackend
	flowStore         *oauth.FlowStore
	registry          *oauth.ProviderRegistry
	invalidator       RunnerInvalidator // optional; nil = no invalidation
	corsOrigin        string
	log               *slog.Logger
	providerPluginIDs map[string]string // provider ID → plugin ID
}

// NewService creates a credentials service. vaultSvc may be nil if the vault
// is not configured (methods that need it return errors).
func NewService(
	vaultSvc *vault.Service,
	pluginCfg pluginhost.ConfigBackend,
	flowStore *oauth.FlowStore,
	corsOrigin string,
) *Service {
	return &Service{
		vaultSvc:   vaultSvc,
		pluginCfg:  pluginCfg,
		flowStore:  flowStore,
		corsOrigin: corsOrigin,
		log:        slog.With("component", "credentials"),
	}
}

// SetRegistry wires the OAuth provider registry used for generic provider operations.
func (s *Service) SetRegistry(r *oauth.ProviderRegistry) {
	s.registry = r
}

// SetProviderPluginIDs maps each provider ID to the plugin ID that supplies its
// OAuth credentials. Populated from manifest oauth_provider fields at startup.
func (s *Service) SetProviderPluginIDs(m map[string]string) {
	s.providerPluginIDs = m
}

// SetVaultService sets or replaces the vault service at runtime (e.g. after startup).
func (s *Service) SetVaultService(svc *vault.Service) {
	s.vaultSvc = svc
}

// SetInvalidator wires the runner invalidator (usually *agent.PoolManager).
func (s *Service) SetInvalidator(inv RunnerInvalidator) {
	s.invalidator = inv
}

// InvalidateUser closes all live runners for userID across all pools.
func (s *Service) InvalidateUser(userID int64) error {
	if s.invalidator == nil {
		return nil
	}
	return s.invalidator.InvalidateUser(userID)
}

// getBroker constructs a flow broker for providerID on demand from the registry
// config and current plugin credentials. The flowType selects which flow entry to
// use when a provider declares multiple flows.
func (s *Service) getBroker(ctx context.Context, providerID string, flowType string) (oauth.FlowBroker, error) {
	return s.getBrokerWithOrigin(ctx, providerID, flowType, "")
}

func (s *Service) getBrokerWithOrigin(ctx context.Context, providerID string, flowType string, origin string) (oauth.FlowBroker, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("provider registry not set")
	}
	providerCfg, ok := s.registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}

	clientID, clientSecret, redirectURI, err := s.providerCredentialsWithOrigin(ctx, providerID, origin)
	if err != nil {
		return nil, err
	}

	var flow oauth.ProviderFlowConfig
	for _, f := range providerCfg.Flows {
		if f.Type == flowType {
			flow = f
			break
		}
	}
	if flow.Type == "" {
		// Fall back to the first flow of the requested type, or any flow.
		for _, f := range providerCfg.Flows {
			if flow.Type == "" {
				flow = f
			}
			if f.Type == flowType {
				flow = f
				break
			}
		}
	}
	if flow.Type == "" {
		return nil, fmt.Errorf("provider %s has no flows configured", providerID)
	}

	endpoint := oauth2.Endpoint{
		TokenURL: flow.TokenURL,
	}
	if flow.AuthStyle == oauth2.AuthStyleInParams {
		endpoint.AuthStyle = oauth2.AuthStyleInParams
	}

	switch flow.Type {
	case "authorization_code":
		endpoint.AuthURL = flow.AuthURL
		cfg := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Scopes:       providerCfg.Scopes,
			Endpoint:     endpoint,
		}
		return oauth.NewAuthCodeBroker(cfg, s.flowStore), nil
	case "device_code":
		endpoint.DeviceAuthURL = flow.DeviceAuthURL
		cfg := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Scopes:       providerCfg.Scopes,
			Endpoint:     endpoint,
		}
		return oauth.NewDeviceCodeBroker(cfg, s.flowStore), nil
	default:
		return nil, fmt.Errorf("provider %s: unknown flow type %q", providerID, flow.Type)
	}
}

func (s *Service) providerCredentials(ctx context.Context, providerID string) (clientID, clientSecret, redirectURI string, err error) {
	return s.providerCredentialsWithOrigin(ctx, providerID, "")
}

func (s *Service) providerCredentialsWithOrigin(ctx context.Context, providerID string, origin string) (clientID, clientSecret, redirectURI string, err error) {
	baseOrigin := s.corsOrigin
	if origin != "" {
		baseOrigin = origin
	}
	if providerID == githubProviderID {
		return ghOAuthClientID, "", baseOrigin + "/api/auth/profile/oauth/" + providerID + "/callback", nil
	}

	pluginID, ok := s.providerPluginIDs[providerID]
	if !ok {
		return "", "", "", fmt.Errorf("no plugin configured for provider: %s", providerID)
	}

	state, err := s.pluginCfg.Get(ctx, pluginID)
	if err != nil {
		return "", "", "", fmt.Errorf("plugin config unavailable for %s: %w", pluginID, err)
	}
	if !state.Enabled {
		return "", "", "", fmt.Errorf("%s plugin is not enabled", pluginID)
	}
	if brand, _ := state.Config["brand"].(string); brand != "" && (providerID == "lark" || providerID == "feishu") && brand != providerID {
		return "", "", "", fmt.Errorf("%s plugin brand is %q; connect provider %q instead", pluginID, brand, brand)
	}

	clientID, _ = state.Config["client_id"].(string)
	if clientID == "" {
		clientID, _ = state.Config["app_id"].(string)
	}
	clientSecret, _ = state.Config["client_secret"].(string)
	if clientSecret == "" {
		clientSecret, _ = state.Config["app_secret"].(string)
	}
	if clientID == "" || clientSecret == "" {
		return "", "", "", fmt.Errorf("%s OAuth app is not configured (set client_id/client_secret or app_id/app_secret in %s plugin)", providerID, pluginID)
	}

	redirectURI, _ = state.Config["redirect_url"].(string)
	if redirectURI == "" {
		redirectURI = baseOrigin + "/api/auth/profile/oauth/" + providerID + "/callback"
	}
	return clientID, clientSecret, redirectURI, nil
}

// saveToken converts an oauth2.Token into an OAuthBundle and persists it under
// the provider's registered vault key.
func (s *Service) saveToken(ctx context.Context, providerID string, userID int64, tok *oauth2.Token) error {
	providerCfg, ok := s.registry.Get(providerID)
	if !ok {
		return fmt.Errorf("unknown provider: %s", providerID)
	}

	clientID, clientSecret, _, err := s.providerCredentials(ctx, providerID)
	if err != nil {
		return err
	}

	bundle := oauth.OAuthBundle{
		Version:         1,
		ClientID:        clientID,
		ClientSecret:    clientSecret,
		AccessToken:     tok.AccessToken,
		RefreshToken:    tok.RefreshToken,
		AccessExpiresAt: tok.Expiry,
	}
	if ri, ok := tok.Extra("refresh_token_expires_in").(float64); ok && ri > 0 {
		bundle.RefreshExpiresAt = time.Now().Add(time.Duration(ri) * time.Second)
	}
	switch providerID {
	case "lark":
		bundle.Brand = "lark"
	case "feishu":
		bundle.Brand = "feishu"
	}

	return oauth.SaveOAuthBundle(ctx, s.vaultSvc, userID, providerCfg.VaultKey, bundle)
}

// --- vault operations ---

// ListVault returns metadata for all vault entries owned by userID.
func (s *Service) ListVault(ctx context.Context, userID int64) ([]VaultEntry, error) {
	if s.vaultSvc == nil {
		return nil, fmt.Errorf("vault not configured")
	}
	entries, err := s.vaultSvc.List(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]VaultEntry, len(entries))
	for i, e := range entries {
		out[i] = VaultEntry{Name: e.Name, UpdatedAt: e.UpdatedAt}
	}
	return out, nil
}

// DeleteVaultEntry removes a named vault entry for userID.
func (s *Service) DeleteVaultEntry(ctx context.Context, userID int64, name string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}
	return s.vaultSvc.Delete(ctx, userID, name)
}

// AddSecretInstruction returns a user-facing instruction to store a secret via
// /config. It never accepts or echoes the secret value.
func (s *Service) AddSecretInstruction(name, purpose string) AddSecretInstruction {
	return AddSecretInstruction{
		Name:    name,
		Purpose: purpose,
		Command: fmt.Sprintf("/config %s <value>", name),
	}
}

// --- OAuth provider status ---

// GetProviderStatuses returns status for all registered OAuth providers.
func (s *Service) GetProviderStatuses(ctx context.Context, userID int64) []ProviderStatus {
	if s.registry == nil {
		return nil
	}
	ids := s.registry.IDs()
	out := make([]ProviderStatus, 0, len(ids))
	for _, p := range ids {
		ps := s.getProviderStatus(ctx, userID, p)
		out = append(out, ps)
	}
	return out
}

func (s *Service) getProviderStatus(ctx context.Context, userID int64, provider string) ProviderStatus {
	ps := ProviderStatus{Provider: provider}

	if s.registry == nil {
		ps.Unavailable = "provider registry not set"
		return ps
	}
	providerCfg, ok := s.registry.Get(provider)
	if !ok {
		ps.Unavailable = fmt.Sprintf("unknown provider: %s", provider)
		return ps
	}

	_, err := s.getBroker(ctx, provider, "")
	if err != nil {
		ps.Unavailable = err.Error()
		return ps
	}
	ps.Available = true

	if s.vaultSvc == nil {
		return ps
	}

	bundle, err := oauth.LoadOAuthBundle(ctx, s.vaultSvc, userID, providerCfg.VaultKey)
	if err != nil {
		s.log.Warn("load oauth bundle", "provider", provider, "user_id", userID, "error", err)
		return ps
	}
	if bundle != nil {
		ps.Connected = true
		if bundle.ClientID != "" {
			ps.Username = bundle.ClientID
		}
		if bundle.Brand != "" {
			ps.Username = bundle.Brand + ":" + bundle.ClientID
		}
	}
	return ps
}

// --- OAuth flow operations ---

// StartFlow starts an OAuth flow for the given provider and user.
// It prefers device_code flows when available; otherwise falls back to authorization_code.
func (s *Service) StartFlow(ctx context.Context, userID int64, provider string) (FlowStatus, error) {
	return s.StartFlowWithOrigin(ctx, userID, provider, "")
}

func (s *Service) StartFlowWithOrigin(ctx context.Context, userID int64, provider string, origin string) (FlowStatus, error) {
	if s.vaultSvc == nil {
		return FlowStatus{}, fmt.Errorf("vault not configured")
	}

	flowType := "authorization_code"
	if s.registry != nil {
		if cfg, ok := s.registry.Get(provider); ok {
			for _, f := range cfg.Flows {
				if f.Type == "device_code" {
					flowType = "device_code"
					break
				}
			}
		}
	}

	broker, err := s.getBrokerWithOrigin(ctx, provider, flowType, origin)
	if err != nil {
		return FlowStatus{}, err
	}

	status, err := broker.StartFlow(ctx, oauth.Provider(provider), userID)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("start %s %s flow: %w", provider, flowType, err)
	}
	return toFlowStatus(status), nil
}

// PollFlow polls an in-flight OAuth flow. For device-code flows it completes and
// saves the token when authorized. For auth-code flows it returns completed=true
// once the callback has finalized the flow.
func (s *Service) PollFlow(ctx context.Context, userID int64, provider, flowID string) (FlowStatus, bool, error) {
	if s.vaultSvc == nil {
		return FlowStatus{}, false, fmt.Errorf("vault not configured")
	}

	// Verify flow ownership.
	flow, ok := s.flowStore.Get(flowID)
	if !ok {
		return FlowStatus{}, false, fmt.Errorf("unknown or expired flow")
	}
	if flow.UserID != userID {
		return FlowStatus{}, false, fmt.Errorf("flow does not belong to this user")
	}

	broker, err := s.getBroker(ctx, provider, "")
	if err != nil {
		return FlowStatus{}, false, err
	}

	status, err := broker.Poll(ctx, flowID)
	if err != nil {
		return FlowStatus{}, false, err
	}

	if status.State == oauth.FlowStateAuthorized {
		// Device-code flows hold the token internally; complete and save here.
		if dc, ok := broker.(*oauth.DeviceCodeBroker); ok {
			tok, err := dc.Complete(ctx, flowID)
			if err != nil {
				return FlowStatus{}, false, fmt.Errorf("complete %s flow: %w", provider, err)
			}
			if err := s.saveToken(ctx, provider, userID, tok); err != nil {
				return FlowStatus{}, false, fmt.Errorf("save %s token: %w", provider, err)
			}
		}
		s.flowStore.Delete(flowID)
		_ = s.InvalidateUser(userID)
		return toFlowStatus(status), true, nil
	}

	return toFlowStatus(status), false, nil
}

// CompleteAuthCodeFlow finalizes an authorization-code OAuth callback flow.
func (s *Service) CompleteAuthCodeFlow(ctx context.Context, provider, flowID, code string) error {
	return s.CompleteAuthCodeFlowWithOrigin(ctx, provider, flowID, code, "")
}

func (s *Service) CompleteAuthCodeFlowWithOrigin(ctx context.Context, provider, flowID, code string, origin string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}

	flow, ok := s.flowStore.Get(flowID)
	if !ok {
		return fmt.Errorf("unknown or expired flow")
	}

	broker, err := s.getBrokerWithOrigin(ctx, provider, "authorization_code", origin)
	if err != nil {
		return err
	}

	ac, ok := broker.(*oauth.AuthCodeBroker)
	if !ok {
		return fmt.Errorf("provider %s does not support authorization_code flow", provider)
	}

	tok, err := ac.Complete(ctx, flowID, code)
	if err != nil {
		return fmt.Errorf("complete %s flow: %w", provider, err)
	}

	if err := s.saveToken(ctx, provider, flow.UserID, tok); err != nil {
		return fmt.Errorf("save %s token: %w", provider, err)
	}
	return nil
}

// Disconnect removes the OAuth bundle for the given provider and user.
func (s *Service) Disconnect(ctx context.Context, userID int64, provider string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}
	if s.registry == nil {
		return fmt.Errorf("provider registry not set")
	}
	providerCfg, ok := s.registry.Get(provider)
	if !ok {
		return fmt.Errorf("unknown provider: %s", provider)
	}
	if err := oauth.DeleteBundle(ctx, s.vaultSvc, userID, providerCfg.VaultKey); err != nil {
		return err
	}
	_ = s.InvalidateUser(userID)
	return nil
}

// GetFlowForCallback returns the stored flow (for callback handlers that need userID).
func (s *Service) GetFlowForCallback(flowID string) (oauth.FlowStatus, bool) {
	return s.flowStore.Get(flowID)
}

func toFlowStatus(fs oauth.FlowStatus) FlowStatus {
	return FlowStatus{
		Provider:        string(fs.Provider),
		FlowID:          fs.FlowID,
		VerificationURI: fs.VerificationURI,
		UserCode:        fs.UserCode,
		ExpiresAt:       fs.ExpiresAt,
		State:           string(fs.State),
	}
}
