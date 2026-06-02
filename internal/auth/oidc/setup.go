package oidc

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/auth/oidc/local"
)

// SetupParams contains dependencies for OIDC setup.
type SetupParams struct {
	DB       *sql.DB
	Store    local.SettingStore
	BaseURL  string
	VaultKey string
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
	auth.OIDCCodeStore
	auth.OIDCAccessTokenStore
}

// SetupResult contains the OIDC components created by Setup.
type SetupResult struct {
	Providers  []auth.AuthProvider
	AuthSvc    *auth.AuthService
	SessionMgr *auth.SessionManager
	StateMgr   *StateManager
	LocalAuth  *local.Service
	// RegisterRoutes mounts local OIDC issuer endpoints on the mux.
	// Nil for external OIDC providers.
	RegisterRoutes func(mux *http.ServeMux)
}

// Setup configures OIDC authentication. When OIDC_ISSUER_URL is set it
// connects to the external provider; otherwise it auto-configures the
// built-in local issuer.
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

	if os.Getenv("OIDC_ISSUER_URL") != "" {
		return setupExternal(ctx, authSvc, sessionMgr, stateMgr)
	}
	return setupLocal(ctx, p, authSvc, sessionMgr, stateMgr)
}

func setupExternal(ctx context.Context, authSvc *auth.AuthService, sessionMgr *auth.SessionManager, stateMgr *StateManager) (*SetupResult, error) {
	cfg, err := ConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	provider, err := NewProvider(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", cfg.ProviderName, err)
	}
	return &SetupResult{
		Providers:  []auth.AuthProvider{provider},
		AuthSvc:    authSvc,
		SessionMgr: sessionMgr,
		StateMgr:   stateMgr,
	}, nil
}

func setupLocal(ctx context.Context, p SetupParams, authSvc *auth.AuthService, sessionMgr *auth.SessionManager, stateMgr *StateManager) (*SetupResult, error) {
	signingKey, err := local.LoadOrGenerateSigningKey(ctx, p.Store)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}

	callbackURL := p.BaseURL + "/auth/callback/local"
	cfg := &local.Config{
		IssuerURL:           p.BaseURL + "/oidc/local",
		ClientID:            local.AutoClientID,
		SigningKey:          signingKey,
		KeyID:               local.AutoKeyID,
		RedirectURIs:        []string{callbackURL},
		AccessTokenTTL:      3600,
		AuthCodeTTL:         120,
		AllowRegistration:   true,
		AllowedEmailDomains: local.SplitTrimmed(os.Getenv("LOCAL_OIDC_ALLOWED_EMAIL_DOMAINS")),
	}

	clientProvider, err := local.NewClientProvider(cfg, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("local client provider: %w", err)
	}

	s := p.AuthStores
	issuerAuthSvc := auth.NewAuthService(p.DB, s, s, s)
	issuerSessionMgr := sessionMgr.WithStore(s)
	localAuth := local.NewService(cfg, s, s, s)
	issuer := local.NewIssuer(cfg, s, s, s, localAuth, issuerAuthSvc, issuerSessionMgr)

	return &SetupResult{
		Providers:  []auth.AuthProvider{clientProvider},
		AuthSvc:    authSvc,
		SessionMgr: sessionMgr,
		StateMgr:   stateMgr,
		LocalAuth:  localAuth,
		RegisterRoutes: func(mux *http.ServeMux) {
			mux.HandleFunc("GET /oidc/local/.well-known/openid-configuration", issuer.HandleDiscovery)
			mux.HandleFunc("GET /oidc/local/jwks.json", issuer.HandleJWKS)
			mux.HandleFunc("GET /oidc/local/authorize", issuer.HandleAuthorize)
			mux.HandleFunc("POST /oidc/local/token", issuer.HandleToken)
			mux.HandleFunc("GET /oidc/local/userinfo", issuer.HandleUserinfo)
		},
	}, nil
}
