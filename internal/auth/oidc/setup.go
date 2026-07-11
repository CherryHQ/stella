package oidc

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
	"github.com/CherryHQ/stella/internal/config"
)

// SetupParams contains dependencies for OIDC setup.
type SetupParams struct {
	DB       *pgxpool.Pool
	BaseURL  string
	VaultKey string
	// OIDC is the static OIDC_* block from the server config snapshot. It drives
	// both the external-vs-local mode decision and the external provider config,
	// so both observe one generation.
	OIDC config.OIDCConfig
	// AuthStores is a store that implements all auth store interfaces.
	// In practice this is *db.OIDCStore.
	AuthStores AuthStores
}

// AuthStores combines all store interfaces needed for OIDC auth setup.
type AuthStores interface {
	auth.UserStore
	auth.LoginIdentityStore
	auth.SessionStore
	auth.CredentialStore
}

// SetupResult contains the OIDC components created by Setup.
type SetupResult struct {
	Providers  []auth.AuthProvider
	AuthSvc    *auth.AuthService
	SessionMgr *auth.SessionManager
	StateMgr   *StateManager
	LocalAuth  *local.Service
}

// Setup configures login authentication. When OIDC_ISSUER_URL is set it
// connects to the external provider; otherwise it enables local password auth.
func Setup(ctx context.Context, p SetupParams) (*SetupResult, error) {
	s := p.AuthStores
	sessionMgr, err := auth.NewSessionManager(s, p.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("session manager: %w", err)
	}
	stateMgr, err := NewStateManager(p.VaultKey)
	if err != nil {
		return nil, fmt.Errorf("state manager: %w", err)
	}
	authSvc := auth.NewAuthService(p.DB, s, s, s)

	if p.OIDC.IssuerURL != "" {
		return setupExternal(ctx, p.OIDC, p.BaseURL, authSvc, sessionMgr, stateMgr)
	}
	return setupLocal(ctx, p, authSvc, sessionMgr, stateMgr)
}

func setupExternal(ctx context.Context, oidcCfg config.OIDCConfig, baseURL string, authSvc *auth.AuthService, sessionMgr *auth.SessionManager, stateMgr *StateManager) (*SetupResult, error) {
	cfg, err := configFrom(oidcCfg)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	provider, err := NewProvider(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", cfg.ProviderName, err)
	}
	providers := []auth.AuthProvider{provider}
	oauthProviders, err := setupOAuthProviders(ctx, baseURL)
	if err != nil {
		return nil, err
	}
	if err := checkProviderNameConflicts(providers, oauthProviders); err != nil {
		return nil, err
	}
	providers = append(providers, oauthProviders...)
	return &SetupResult{
		Providers:  providers,
		AuthSvc:    authSvc,
		SessionMgr: sessionMgr,
		StateMgr:   stateMgr,
	}, nil
}

func checkProviderNameConflicts(existing, added []auth.AuthProvider) error {
	seen := make(map[string]struct{}, len(existing)+len(added))
	for _, p := range existing {
		seen[p.Name()] = struct{}{}
	}
	for _, p := range added {
		if _, ok := seen[p.Name()]; ok {
			return fmt.Errorf("duplicate auth provider name %q", p.Name())
		}
		seen[p.Name()] = struct{}{}
	}
	return nil
}

func setupOAuthProviders(ctx context.Context, baseURL string) ([]auth.AuthProvider, error) {
	if !OAuthConfiguredFromEnv() {
		return nil, nil
	}
	configs, err := OAuthConfigsFromEnv(baseURL)
	if err != nil {
		return nil, fmt.Errorf("oauth providers: %w", err)
	}
	providers := make([]auth.AuthProvider, 0, len(configs))
	for _, cfg := range configs {
		var provider auth.AuthProvider
		if cfg.Kind == "google" {
			p, err := NewProvider(ctx, &Config{
				ProviderName: cfg.ProviderName,
				IssuerURL:    "https://accounts.google.com",
				ClientID:     cfg.ClientID,
				ClientSecret: cfg.ClientSecret,
				RedirectURL:  cfg.RedirectURL,
				Scopes:       cfg.Scopes,
			})
			if err != nil {
				return nil, fmt.Errorf("oauth provider %q: %w", cfg.ProviderName, err)
			}
			provider = &emailDomainProvider{provider: p, allowedDomains: cfg.AllowedEmailDomains}
		} else {
			p, err := NewOAuthProvider(cfg)
			if err != nil {
				return nil, fmt.Errorf("oauth provider %q: %w", cfg.ProviderName, err)
			}
			provider = p
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

type emailDomainProvider struct {
	provider       auth.AuthProvider
	allowedDomains []string
}

func (p *emailDomainProvider) Name() string { return p.provider.Name() }

func (p *emailDomainProvider) LoginURL(ctx context.Context, state auth.AuthState) (string, error) {
	return p.provider.LoginURL(ctx, state)
}

func (p *emailDomainProvider) HandleCallback(ctx context.Context, r *http.Request, state auth.AuthState) (*auth.ExternalIdentity, error) {
	identity, err := p.provider.HandleCallback(ctx, r, state)
	if err != nil {
		return nil, err
	}
	if len(p.allowedDomains) > 0 && !emailDomainAllowed(identity.Email, p.allowedDomains) {
		return nil, fmt.Errorf("oauth login: email domain not allowed")
	}
	return identity, nil
}

func setupLocal(ctx context.Context, p SetupParams, authSvc *auth.AuthService, sessionMgr *auth.SessionManager, stateMgr *StateManager) (*SetupResult, error) {
	cfg := &local.Config{
		AllowRegistration:     local.AllowRegistrationFromEnv(localPasswordEnv("ALLOW_REGISTRATION")),
		BootstrapRegistration: true,
		AllowedEmailDomains:   local.SplitTrimmed(localPasswordEnv("ALLOWED_EMAIL_DOMAINS")),
	}

	s := p.AuthStores
	localAuth := local.NewService(cfg, s, s)

	oauthProviders, err := setupOAuthProviders(ctx, p.BaseURL)
	if err != nil {
		return nil, err
	}
	if err := checkProviderNameConflicts(nil, oauthProviders); err != nil {
		return nil, err
	}

	return &SetupResult{
		Providers:  oauthProviders,
		AuthSvc:    authSvc,
		SessionMgr: sessionMgr,
		StateMgr:   stateMgr,
		LocalAuth:  localAuth,
	}, nil
}

func localPasswordEnv(name string) string {
	if v := os.Getenv("LOCAL_PASSWORD_" + name); v != "" {
		return v
	}
	return os.Getenv("LOCAL_OIDC_" + name)
}
