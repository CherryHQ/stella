package credentials

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	oauth "github.com/vaayne/anna/internal/credentials/oauth"
	"github.com/vaayne/anna/internal/pluginhost"
	"github.com/vaayne/anna/internal/vault"
	pkgplugins "github.com/vaayne/anna/pkg/plugins"
)

const (
	pluginIDGitHub       = "tool/gh"
	pluginIDGitHubLegacy = "auth/github"
	pluginIDLark         = "tool/lark-cli"
	pluginIDLarkLegacy   = "auth/lark"

	larkCallbackPath = "/api/auth/profile/oauth/lark/callback"
	ghCallbackPath   = "/api/auth/profile/oauth/github/callback"
)

// Service is the shared host-side credential manager. It owns vault secret
// operations and OAuth orchestration. Admin HTTP handlers and the built-in
// credentials tool both delegate to this service.
type Service struct {
	vaultSvc    *vault.Service
	pluginCfg   pluginhost.ConfigBackend
	flowStore   *oauth.FlowStore
	invalidator RunnerInvalidator // optional; nil = no invalidation
	corsOrigin  string
	log         *slog.Logger

	mu                    sync.Mutex
	ghBroker              *oauth.GitHubBroker
	ghBrokerClientID      string
	ghBrokerRedirectURI   string
	larkBroker            *oauth.LarkBroker
	larkBrokerAppID       string
	larkBrokerRedirectURI string
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

// --- broker helpers (migrated from admin.Server) ---

func (s *Service) getGitHubBroker(ctx context.Context) (*oauth.GitHubBroker, error) {
	state, sourceID, err := s.oauthPluginState(ctx, pluginIDGitHub, pluginIDGitHubLegacy, func(cfg map[string]any) bool {
		clientID, _ := cfg["client_id"].(string)
		clientSecret, _ := cfg["client_secret"].(string)
		return clientID != "" && clientSecret != ""
	})
	if err != nil {
		return nil, fmt.Errorf("github plugin config unavailable: %w", err)
	}
	if !state.Enabled {
		return nil, fmt.Errorf("%s plugin is not enabled", sourceID)
	}
	clientID, _ := state.Config["client_id"].(string)
	clientSecret, _ := state.Config["client_secret"].(string)
	if clientID == "" || clientSecret == "" {
		return nil, fmt.Errorf("github OAuth app is not configured (set client_id and client_secret in tool/gh plugin)")
	}
	redirectURI, _ := state.Config["redirect_url"].(string)
	if redirectURI == "" {
		redirectURI = s.corsOrigin + ghCallbackPath
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ghBroker == nil || s.ghBrokerClientID != clientID || s.ghBrokerRedirectURI != redirectURI {
		s.ghBroker = oauth.NewGitHubBroker(oauth.GitHubConfig{
			ClientID:     clientID,
			ClientSecret: clientSecret,
		}, s.flowStore).WithRedirectURI(redirectURI)
		s.ghBrokerClientID = clientID
		s.ghBrokerRedirectURI = redirectURI
	}
	return s.ghBroker, nil
}

func (s *Service) getLarkBroker(ctx context.Context) (*oauth.LarkBroker, error) {
	state, sourceID, err := s.oauthPluginState(ctx, pluginIDLark, pluginIDLarkLegacy, func(cfg map[string]any) bool {
		appID, _ := cfg["app_id"].(string)
		appSecret, _ := cfg["app_secret"].(string)
		return appID != "" && appSecret != ""
	})
	if err != nil {
		return nil, fmt.Errorf("lark plugin config unavailable: %w", err)
	}
	if !state.Enabled {
		return nil, fmt.Errorf("%s plugin is not enabled", sourceID)
	}
	appID, _ := state.Config["app_id"].(string)
	appSecret, _ := state.Config["app_secret"].(string)
	brand, _ := state.Config["brand"].(string)
	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("lark OAuth app is not configured (set app_id and app_secret in tool/lark-cli plugin)")
	}
	if brand == "" {
		brand = "lark"
	}
	redirectURI, _ := state.Config["redirect_url"].(string)
	if redirectURI == "" {
		redirectURI = s.corsOrigin + larkCallbackPath
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.larkBroker == nil || s.larkBrokerAppID != appID || s.larkBrokerRedirectURI != redirectURI {
		s.larkBroker = oauth.NewLarkBroker(oauth.LarkConfig{
			AppID:     appID,
			AppSecret: appSecret,
			Brand:     brand,
		}, s.flowStore).WithRedirectURI(redirectURI)
		s.larkBrokerAppID = appID
		s.larkBrokerRedirectURI = redirectURI
	}
	return s.larkBroker, nil
}

func (s *Service) oauthPluginState(ctx context.Context, primaryID, legacyID string, configured func(map[string]any) bool) (pkgplugins.PluginState, string, error) {
	state, err := s.pluginCfg.Get(ctx, primaryID)
	if err == nil && (configured == nil || configured(state.Config) || !state.Enabled) {
		return state, primaryID, nil
	}
	legacy, legacyErr := s.pluginCfg.Get(ctx, legacyID)
	if legacyErr == nil {
		return legacy, legacyID, nil
	}
	if err == nil {
		return state, primaryID, nil
	}
	return pkgplugins.PluginState{}, primaryID, err
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

// GetProviderStatuses returns status for all known OAuth providers.
func (s *Service) GetProviderStatuses(ctx context.Context, userID int64) []ProviderStatus {
	providers := []string{"github", "lark"}
	out := make([]ProviderStatus, 0, len(providers))
	for _, p := range providers {
		ps := s.getProviderStatus(ctx, userID, p)
		out = append(out, ps)
	}
	return out
}

func (s *Service) getProviderStatus(ctx context.Context, userID int64, provider string) ProviderStatus {
	ps := ProviderStatus{Provider: provider}

	switch provider {
	case "github":
		_, err := s.getGitHubBroker(ctx)
		if err != nil {
			ps.Unavailable = err.Error()
			return ps
		}
		ps.Available = true
		bundle, err := oauth.LoadGHBundle(ctx, s.vaultSvc, userID)
		if err != nil {
			s.log.Warn("load gh bundle", "user_id", userID, "error", err)
			return ps
		}
		if bundle != nil {
			ps.Connected = true
		}

	case "lark":
		_, err := s.getLarkBroker(ctx)
		if err != nil {
			ps.Unavailable = err.Error()
			return ps
		}
		ps.Available = true
		bundle, err := oauth.LoadLarkBundle(ctx, s.vaultSvc, userID)
		if err != nil {
			s.log.Warn("load lark bundle", "user_id", userID, "error", err)
			return ps
		}
		if bundle != nil {
			ps.Connected = true
			label := bundle.AppID
			if bundle.Brand != "" {
				label = bundle.Brand + ":" + bundle.AppID
			}
			ps.Username = label
		}
	}
	return ps
}

// --- OAuth flow operations ---

// StartFlow starts a device-flow for the given provider and user.
func (s *Service) StartFlow(ctx context.Context, userID int64, provider string) (FlowStatus, error) {
	if s.vaultSvc == nil {
		return FlowStatus{}, fmt.Errorf("vault not configured")
	}
	switch provider {
	case "github":
		broker, err := s.getGitHubBroker(ctx)
		if err != nil {
			return FlowStatus{}, err
		}
		status, err := broker.StartDeviceFlow(ctx, userID)
		if err != nil {
			return FlowStatus{}, fmt.Errorf("start github device flow: %w", err)
		}
		return toFlowStatus(status), nil

	case "lark":
		broker, err := s.getLarkBroker(ctx)
		if err != nil {
			return FlowStatus{}, err
		}
		status, err := broker.StartDeviceFlow(ctx, userID)
		if err != nil {
			return FlowStatus{}, fmt.Errorf("start lark device flow: %w", err)
		}
		return toFlowStatus(status), nil

	default:
		return FlowStatus{}, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// PollFlow polls an in-flight device flow. For GitHub it also completes the flow
// when authorized. Returns the updated status and whether the flow completed.
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

	switch provider {
	case "github":
		broker, err := s.getGitHubBroker(ctx)
		if err != nil {
			return FlowStatus{}, false, err
		}
		status, err := broker.Poll(ctx, flowID)
		if err != nil {
			return FlowStatus{}, false, err
		}
		completed := false
		if status.State == oauth.FlowStateAuthorized {
			if cerr := broker.Complete(ctx, s.vaultSvc, userID, flowID); cerr != nil {
				return FlowStatus{}, false, fmt.Errorf("complete github flow: %w", cerr)
			}
			completed = true
			_ = s.InvalidateUser(userID)
		}
		return toFlowStatus(status), completed, nil

	case "lark":
		broker, err := s.getLarkBroker(ctx)
		if err != nil {
			return FlowStatus{}, false, err
		}
		status, err := broker.Poll(ctx, flowID)
		if err != nil {
			return FlowStatus{}, false, err
		}
		if status.State == oauth.FlowStateAuthorized {
			s.flowStore.Delete(flowID)
			_ = s.InvalidateUser(userID)
			return toFlowStatus(status), true, nil
		}
		return toFlowStatus(status), false, nil

	default:
		return FlowStatus{}, false, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// CompleteLarkFlow finalizes a Lark OAuth callback flow.
func (s *Service) CompleteLarkFlow(ctx context.Context, userID int64, flowID, code string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}
	broker, err := s.getLarkBroker(ctx)
	if err != nil {
		return err
	}
	if err := broker.Complete(ctx, s.vaultSvc, userID, flowID, code); err != nil {
		return fmt.Errorf("complete lark flow: %w", err)
	}
	return nil
}

// Disconnect removes the OAuth bundle for the given provider and user.
func (s *Service) Disconnect(ctx context.Context, userID int64, provider string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}
	var key string
	switch provider {
	case "github":
		key = oauth.VaultKeyGitHub
	case "lark":
		key = oauth.VaultKeyLark
	default:
		return fmt.Errorf("unsupported provider: %s", provider)
	}
	if err := oauth.DeleteBundle(ctx, s.vaultSvc, userID, key); err != nil {
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
