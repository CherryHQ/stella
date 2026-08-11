package connections

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	oauth "github.com/CherryHQ/stella/internal/connections/oauth"
	"github.com/CherryHQ/stella/internal/vault"
	pkgdb "github.com/CherryHQ/stella/pkg/db/sqlc"
)

// Service is the shared host-side credential manager. It owns vault secret
// operations and OAuth orchestration. The user-facing HTTP handlers and the
// built-in oauth tool both reach it through the Authority-bound Access
// (Service.Access), which scopes every operation to the acting user; admin
// provider-config CRUD is a control-plane concern gated separately, and the
// OAuth callback / token-refresh paths are trusted internal callers of the raw
// Service methods.
type Service struct {
	vaultSvc    *vault.Service
	q           *pkgdb.Queries
	flowStore   *oauth.FlowStore
	registry    *oauth.ProviderRegistry
	invalidator RunnerInvalidator // optional; nil = no invalidation
	corsOrigin  string
	log         *slog.Logger
}

// NewService creates a credentials service. vaultSvc and q may be nil if the
// vault / DB is not yet configured (methods that need them return errors).
func NewService(
	vaultSvc *vault.Service,
	q *pkgdb.Queries,
	flowStore *oauth.FlowStore,
	corsOrigin string,
) *Service {
	return &Service{
		vaultSvc:   vaultSvc,
		q:          q,
		flowStore:  flowStore,
		corsOrigin: corsOrigin,
		log:        slog.With("component", "credentials"),
	}
}

// NewServiceForPool creates a credentials service that owns the sqlc query set
// for the connections tables, so callers pass only the pgx pool.
func NewServiceForPool(
	vaultSvc *vault.Service,
	pool *pgxpool.Pool,
	flowStore *oauth.FlowStore,
	corsOrigin string,
) *Service {
	return NewService(vaultSvc, pkgdb.New(pool), flowStore, corsOrigin)
}

// SetRegistry wires the OAuth provider registry used for generic provider operations.
func (s *Service) SetRegistry(r *oauth.ProviderRegistry) {
	s.registry = r
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
func (s *Service) InvalidateUser(userID string) error {
	if s.invalidator == nil {
		return nil
	}
	return s.invalidator.InvalidateUser(userID)
}

// InvalidateAgent closes all live runners for one agent across every user.
func (s *Service) InvalidateAgent(agentID string) error {
	if s.invalidator == nil {
		return nil
	}
	return s.invalidator.InvalidateAgent(agentID)
}

// InvalidateAll closes every live runner across all agents and users.
func (s *Service) InvalidateAll() error {
	if s.invalidator == nil {
		return nil
	}
	return s.invalidator.InvalidateAll()
}

// getBroker constructs a flow broker for providerID on demand from the registry
// config and current plugin credentials. The flowType selects which flow entry to
// use when a provider declares multiple flows.
func (s *Service) getBroker(ctx context.Context, providerID string, flowType string, scopes []string) (oauth.FlowBroker, error) {
	return s.getBrokerWithOrigin(ctx, providerID, flowType, "", scopes)
}

func (s *Service) getBrokerWithOrigin(ctx context.Context, providerID string, flowType string, origin string, scopes []string) (oauth.FlowBroker, error) {
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

	if scopes == nil {
		// DB override wins over the YAML default, independent of the credential gate.
		scopes = s.providerScopes(ctx, providerID)
	}

	switch flow.Type {
	case "authorization_code":
		endpoint.AuthURL = flow.AuthURL
		cfg := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Scopes:       scopes,
			Endpoint:     endpoint,
		}
		return oauth.NewAuthCodeBroker(cfg, s.flowStore, flow.PKCE), nil
	case "device_code":
		endpoint.DeviceAuthURL = flow.DeviceAuthURL
		cfg := &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURI,
			Scopes:       scopes,
			Endpoint:     endpoint,
		}
		return oauth.NewDeviceCodeBroker(cfg, s.flowStore, s.persistDeviceToken), nil
	default:
		return nil, fmt.Errorf("provider %s: unknown flow type %q", providerID, flow.Type)
	}
}

func (s *Service) providerCredentials(ctx context.Context, providerID string) (clientID, clientSecret, redirectURI string, err error) {
	return s.providerCredentialsWithOrigin(ctx, providerID, "")
}

func (s *Service) providerCredentialsWithOrigin(_ context.Context, providerID string, origin string) (clientID, clientSecret, redirectURI string, err error) {
	baseOrigin := s.corsOrigin
	if origin != "" {
		baseOrigin = origin
	}
	redirectFallback := baseOrigin + "/api/auth/oauth/" + providerID + "/callback"

	// DB override wins over YAML default.
	if s.q != nil {
		cfg, err := s.q.GetAuthOAuthProvider(context.Background(), providerID)
		if err == nil && cfg.ClientID != "" {
			secret := ""
			if cfg.ClientSecretEnc != "" && s.vaultSvc != nil {
				var err error
				secret, err = s.vaultSvc.DecryptSystem(cfg.ClientSecretEnc)
				if err != nil {
					s.log.Error("decrypt provider client_secret", "provider", providerID, "error", err)
				}
			}
			redirectURL := cfg.RedirectUrl
			if redirectURL == "" {
				redirectURL = redirectFallback
			}
			return cfg.ClientID, secret, redirectURL, nil
		}
	}

	// Fall back to YAML defaults baked into the registry at startup.
	if s.registry != nil {
		if providerCfg, ok := s.registry.Get(providerID); ok && providerCfg.ClientID != "" {
			return providerCfg.ClientID, providerCfg.ClientSecret, redirectFallback, nil
		}
	}

	return "", "", "", fmt.Errorf("oauth credentials not configured for provider %q — set client_id and client_secret on the Credentials page", providerID)
}

// providerScopes returns the effective OAuth scopes for providerID: the DB
// override when a row exists with a non-empty scopes array, otherwise the YAML
// seed default (D2). Resolution is independent of the client_id credential gate,
// so an admin can override scopes without also overriding credentials. Both
// sources are normalized here rather than trusted: the admin write path cleans
// its input, but a hand-edited manifest is not a write path.
func (s *Service) providerScopes(ctx context.Context, providerID string) []string {
	if s.q != nil {
		if cfg, err := s.q.GetAuthOAuthProvider(ctx, providerID); err == nil {
			if scopes := normalizeScopes(cfg.Scopes); len(scopes) > 0 {
				return scopes
			}
		}
	}
	if s.registry != nil {
		if providerCfg, ok := s.registry.Get(providerID); ok {
			return normalizeScopes(providerCfg.Scopes)
		}
	}
	return nil
}

// desiredScopes resolves the complete scope set the next authorization for
// userID must request: the administrator-configured minimum, the scopes this
// user already asked for, and whatever the caller is adding now. Stella does not
// cap the result — the provider's app configuration and consent screen are the
// authority on what a user can actually grant, and a scope the provider refuses
// simply comes back missing from the grant (see reconnectDecision).
func (s *Service) desiredScopes(ctx context.Context, userID, providerID string, requested []string) ([]string, error) {
	if s.registry == nil {
		return nil, fmt.Errorf("provider registry not set")
	}
	providerCfg, ok := s.registry.Get(providerID)
	if !ok {
		return nil, fmt.Errorf("unknown provider: %s", providerID)
	}
	base := s.providerScopes(ctx, providerID)
	if s.vaultSvc != nil {
		bundle, err := oauth.LoadOAuthBundle(ctx, s.vaultSvc, userID, providerCfg.VaultKey)
		if err != nil {
			return nil, fmt.Errorf("load %s oauth scopes: %w", providerID, err)
		}
		if bundle != nil {
			base = bundleDesiredScopes(bundle, base)
		}
	}
	return unionScopes(base, requested), nil
}

func bundleDesiredScopes(bundle *oauth.OAuthBundle, defaults []string) []string {
	if len(bundle.DesiredScopes) > 0 {
		return unionScopes(defaults, bundle.DesiredScopes)
	}
	if bundle.GrantedScope != "" {
		return unionScopes(defaults, strings.Fields(bundle.GrantedScope))
	}
	return append([]string(nil), defaults...)
}

// GetOAuthProviderConfig returns the effective config for a provider:
// DB override merged over the YAML default. ClientSecret is masked.
func (s *Service) GetOAuthProviderConfig(ctx context.Context, providerID string) (OAuthProviderConfig, error) {
	out := OAuthProviderConfig{ProviderID: providerID}

	// YAML default from registry.
	if s.registry != nil {
		if providerCfg, ok := s.registry.Get(providerID); ok {
			out.ClientID = providerCfg.ClientID
			if providerCfg.ClientSecret != "" {
				out.ClientSecret = "***"
			}
			out.DefaultScopes = normalizeScopes(providerCfg.Scopes)
		}
	}

	// DB override (takes precedence). ClientID gate applies to credentials only;
	// the scopes override is surfaced independently (D2).
	if s.q != nil {
		cfg, err := s.q.GetAuthOAuthProvider(ctx, providerID)
		if err == nil {
			if cfg.ClientID != "" {
				out.ClientID = cfg.ClientID
				out.RedirectURL = cfg.RedirectUrl
				if cfg.ClientSecretEnc != "" {
					out.ClientSecret = "***"
				}
			}
			out.Scopes = normalizeScopes(cfg.Scopes)
		}
	}

	return out, nil
}

// SetOAuthProviderConfig persists a provider credential override to the DB.
// The client_secret is encrypted with the master vault key before storage.
func (s *Service) SetOAuthProviderConfig(ctx context.Context, cfg OAuthProviderConfig) error {
	if s.q == nil {
		return fmt.Errorf("database not configured")
	}

	// Existing row drives both secret preservation and credential-change
	// detection. A read error (no row yet) leaves existing zero-valued.
	existing, existingErr := s.q.GetAuthOAuthProvider(ctx, cfg.ProviderID)
	hadRow := existingErr == nil

	secretEnc := ""
	// A new secret was submitted → encrypt and treat as a credential rotation.
	secretSubmitted := cfg.ClientSecret != ""
	if secretSubmitted {
		if s.vaultSvc == nil {
			return fmt.Errorf("vault not configured: cannot encrypt client_secret")
		}
		var err error
		secretEnc, err = s.vaultSvc.EncryptSystem(cfg.ClientSecret)
		if err != nil {
			return fmt.Errorf("encrypt client_secret: %w", err)
		}
	} else if hadRow {
		// Preserve existing encrypted secret when no new value is provided.
		secretEnc = existing.ClientSecretEnc
	}

	// Normalize at the write boundary: trim, drop empties, dedupe (D2). Non-nil
	// so pgx encodes '{}' (no override) rather than NULL.
	scopes := normalizeScopes(cfg.Scopes)
	if scopes == nil {
		scopes = []string{}
	}
	if err := s.q.UpsertAuthOAuthProvider(ctx, pkgdb.UpsertAuthOAuthProviderParams{
		ID:              uuid.Must(uuid.NewV7()).String(),
		ProviderID:      cfg.ProviderID,
		ClientID:        cfg.ClientID,
		ClientSecretEnc: secretEnc,
		RedirectUrl:     cfg.RedirectURL,
		Scopes:          scopes,
	}); err != nil {
		return err
	}

	// Credential rotation must not keep serving sessions built on old
	// credentials (D4). A scope-only change (same client_id, no new secret)
	// leaves existing tokens valid and does not invalidate.
	// global lock: InvalidateAll on provider credential change; scope to affected
	// users if it ever disrupts unrelated sessions.
	credentialChanged := secretSubmitted || (hadRow && existing.ClientID != cfg.ClientID)
	if credentialChanged {
		if err := s.InvalidateAll(); err != nil {
			s.log.Warn("invalidate runners after provider credential change",
				"provider", cfg.ProviderID, "error", err)
		}
	}
	return nil
}

// DeleteOAuthProviderConfig removes the DB override for a provider, reverting to YAML defaults.
func (s *Service) DeleteOAuthProviderConfig(ctx context.Context, providerID string) error {
	if s.q == nil {
		return fmt.Errorf("database not configured")
	}
	return s.q.DeleteAuthOAuthProvider(ctx, providerID)
}

// ProviderClientID returns the effective client_id for the given tool OAuth provider.
func (s *Service) ProviderClientID(ctx context.Context, providerID string) (string, error) {
	clientID, _, _, err := s.providerCredentials(ctx, providerID)
	return clientID, err
}

// saveToken converts an oauth2.Token into an OAuthBundle and persists it under
// the provider's registered vault key.
func (s *Service) saveToken(ctx context.Context, providerID string, userID string, tok *oauth2.Token, desiredScopes []string) error {
	var refreshExpiresAt time.Time
	if ri, ok := tok.Extra("refresh_token_expires_in").(float64); ok && ri > 0 {
		refreshExpiresAt = time.Now().Add(time.Duration(ri) * time.Second)
	}
	grantedScope, _ := tok.Extra("scope").(string)
	return s.saveBundle(ctx, providerID, userID, tok.AccessToken, tok.RefreshToken, tok.Expiry, refreshExpiresAt, grantedScope, desiredScopes)
}

// saveBundle is the shared implementation for persisting an OAuth token bundle.
func (s *Service) saveBundle(ctx context.Context, providerID, userID, accessToken, refreshToken string, accessExpiresAt, refreshExpiresAt time.Time, grantedScope string, desiredScopes []string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}
	if s.registry == nil {
		return fmt.Errorf("provider registry not set")
	}
	clientID, clientSecret, _, err := s.providerCredentials(ctx, providerID)
	if err != nil {
		return err
	}
	bundle := oauth.OAuthBundle{
		Version:          1,
		ClientID:         clientID,
		ClientSecret:     clientSecret,
		AccessToken:      accessToken,
		RefreshToken:     refreshToken,
		AccessExpiresAt:  accessExpiresAt,
		RefreshExpiresAt: refreshExpiresAt,
		DesiredScopes:    normalizeScopes(desiredScopes),
		GrantedScope:     grantedScope,
	}
	switch providerID {
	case "lark":
		bundle.Brand = "lark"
	case "feishu":
		bundle.Brand = "feishu"
	}
	return s.registry.SaveBundle(ctx, s.vaultSvc, providerID, userID, bundle)
}

// --- vault operations ---

// ListVault returns metadata for all vault entries owned by userID.
func (s *Service) ListVault(ctx context.Context, userID string) ([]VaultEntry, error) {
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
func (s *Service) DeleteVaultEntry(ctx context.Context, userID string, name string) error {
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

// GitHubAccessToken returns the access token userID has bound for GitHub, or ""
// when GitHub is not connected (or the vault/registry is unavailable). A
// near-expiry token is refreshed as a side effect. It never surfaces the
// not-connected condition as an error: an empty string means "clone anonymously".
func (s *Service) GitHubAccessToken(ctx context.Context, userID string) string {
	if s.registry == nil || s.vaultSvc == nil {
		return ""
	}
	bundle, err := s.registry.GetToken(ctx, s.vaultSvc, string(oauth.ProviderGitHub), userID, 0)
	if err != nil {
		// Not-connected surfaces here as an error too; log at debug so a genuine
		// vault/refresh failure leaves a breadcrumb without spamming on the common
		// anonymous case.
		s.log.Debug("github access token unavailable", "user_id", userID, "error", err)
		return ""
	}
	if bundle == nil {
		return ""
	}
	return bundle.AccessToken
}

// GetProviderStatuses returns status for all registered OAuth providers.
func (s *Service) GetProviderStatuses(ctx context.Context, userID string) []ProviderStatus {
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

func (s *Service) AnyProviderConfigured(ctx context.Context, userID string) (bool, error) {
	if s.registry == nil {
		return false, nil
	}
	for _, provider := range s.registry.IDs() {
		providerCfg, ok := s.registry.Get(provider)
		if !ok {
			continue
		}
		if providerCfg.ClientID != "" {
			return true, nil
		}
		if s.q != nil {
			if cfg, err := s.q.GetAuthOAuthProvider(ctx, provider); err == nil && cfg.ClientID != "" {
				return true, nil
			}
		}
	}
	return false, nil
}

func (s *Service) getProviderStatus(ctx context.Context, userID string, provider string) ProviderStatus {
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
	ps.Icon = providerCfg.Icon
	ps.RequestedScopes = s.providerScopes(ctx, provider)

	// Configured = DB row exists with a client_id OR YAML has a client_id.
	if providerCfg.ClientID != "" {
		ps.Configured = true
	}
	if s.q != nil {
		if cfg, err := s.q.GetAuthOAuthProvider(ctx, provider); err == nil && cfg.ClientID != "" {
			ps.Configured = true
		}
	}

	_, err := s.getBroker(ctx, provider, "", nil)
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
		// UTC so direct JSON serialization emits RFC3339 with a Z zone.
		ps.AccessExpiresAt = bundle.AccessExpiresAt.UTC()
		ps.RefreshExpiresAt = bundle.RefreshExpiresAt.UTC()

		requested := bundleDesiredScopes(bundle, ps.RequestedScopes)
		ps.RequestedScopes = append([]string(nil), requested...)

		// Empty GrantedScope means unknown (pre-capture bundle), not "no scopes".
		grantedKnown := bundle.GrantedScope != ""
		var granted []string
		if grantedKnown {
			granted = strings.Fields(bundle.GrantedScope)
			ps.GrantedScopes = granted
		}

		// Compare the connected bundle's client_id against the effective one to
		// detect credential rotation (D4). A credentials read error here just
		// leaves needs_reconnect unset — the connection still shows as connected.
		if effectiveClientID, _, _, cerr := s.providerCredentials(ctx, provider); cerr == nil {
			ps.NeedsReconnect, ps.ReconnectReason = reconnectDecision(
				bundle.ClientID, effectiveClientID, requested, granted, grantedKnown)
		}
	}
	return ps
}

// --- OAuth flow operations ---

// preferredFlowType returns "device_code" if the provider has a device_code
// flow registered, otherwise "authorization_code". Used by the agent tool to
// prefer flows that do not require a browser redirect.
func (s *Service) preferredFlowType(provider string) string {
	if s.registry != nil {
		if cfg, ok := s.registry.Get(provider); ok {
			for _, f := range cfg.Flows {
				if f.Type == "device_code" {
					return "device_code"
				}
			}
		}
	}
	return "authorization_code"
}

// StartFlow starts an OAuth flow for the given provider and user.
// It prefers device_code flows when available, making it suitable for agent/CLI use.
func (s *Service) StartFlow(ctx context.Context, userID string, provider string, requestedScopes []string) (FlowStatus, error) {
	if s.vaultSvc == nil {
		return FlowStatus{}, fmt.Errorf("vault not configured")
	}
	flowType := s.preferredFlowType(provider)
	desired, err := s.desiredScopes(ctx, userID, provider, requestedScopes)
	if err != nil {
		return FlowStatus{}, err
	}
	broker, err := s.getBroker(ctx, provider, flowType, desired)
	if err != nil {
		return FlowStatus{}, err
	}
	status, err := broker.StartFlow(ctx, oauth.Provider(provider), userID, desired)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("start %s %s flow: %w", provider, flowType, err)
	}
	return toFlowStatus(status), nil
}

// StartFlowWithOrigin starts an OAuth flow for use by the admin UI.
// It uses the provider's preferred flow type (device_code when available,
// otherwise authorization_code). The callback URL is built from origin so
// browser redirects land on the correct host.
func (s *Service) StartFlowWithOrigin(ctx context.Context, userID string, provider string, origin string, requestedScopes []string) (FlowStatus, error) {
	if s.vaultSvc == nil {
		return FlowStatus{}, fmt.Errorf("vault not configured")
	}
	flowType := s.preferredFlowType(provider)
	desired, err := s.desiredScopes(ctx, userID, provider, requestedScopes)
	if err != nil {
		return FlowStatus{}, err
	}
	broker, err := s.getBrokerWithOrigin(ctx, provider, flowType, origin, desired)
	if err != nil {
		return FlowStatus{}, err
	}
	status, err := broker.StartFlow(ctx, oauth.Provider(provider), userID, desired)
	if err != nil {
		return FlowStatus{}, fmt.Errorf("start %s %s flow: %w", provider, flowType, err)
	}
	return toFlowStatus(status), nil
}

// PollFlow polls an in-flight OAuth flow. For device-code flows it completes and
// saves the token when authorized. For auth-code flows it returns completed=true
// once the callback has finalized the flow.
func (s *Service) PollFlow(ctx context.Context, userID string, provider, flowID string) (FlowStatus, bool, error) {
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
	if string(flow.Provider) != provider {
		return FlowStatus{}, false, fmt.Errorf("unknown or expired flow")
	}

	broker, err := s.getBroker(ctx, provider, flow.FlowType, flow.DesiredScopes)
	if err != nil {
		return FlowStatus{}, false, err
	}

	status, err := broker.Poll(ctx, flowID)
	if err != nil {
		return FlowStatus{}, false, err
	}

	if status.State == oauth.FlowStateAuthorized {
		// Device-code tokens are persisted by persistDeviceToken before the flow
		// is marked authorized; auth-code tokens by the callback handler. Here we
		// only report completion and clean up.
		s.flowStore.Delete(flowID)
		return toFlowStatus(status), true, nil
	}

	return toFlowStatus(status), false, nil
}

// persistDeviceToken saves a device-code token to the vault and refreshes live
// runners. It runs from the broker's background goroutine the moment the user
// authorizes, so the connection is finalized without the client polling.
func (s *Service) persistDeviceToken(flowID string, tok *oauth2.Token) error {
	flow, ok := s.flowStore.Get(flowID)
	if !ok {
		return fmt.Errorf("unknown or expired flow")
	}
	if err := s.saveToken(context.Background(), string(flow.Provider), flow.UserID, tok, flow.DesiredScopes); err != nil {
		return fmt.Errorf("save %s token: %w", flow.Provider, err)
	}
	_ = s.InvalidateUser(flow.UserID)
	return nil
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
	if !ok || string(flow.Provider) != provider {
		return fmt.Errorf("unknown or expired flow")
	}

	broker, err := s.getBrokerWithOrigin(ctx, provider, "authorization_code", origin, flow.DesiredScopes)
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

	if err := s.saveToken(ctx, provider, flow.UserID, tok, flow.DesiredScopes); err != nil {
		s.flowStore.Update(flowID, oauth.FlowStateFailed, func(fs *oauth.FlowStatus) { fs.Error = err.Error() })
		return fmt.Errorf("save %s token: %w", provider, err)
	}
	// Match device-flow ordering: persistence is complete before observers can
	// see an authorized flow and delete its state.
	s.flowStore.Update(flowID, oauth.FlowStateAuthorized, nil)
	// Invalidate live runners so the next session picks up the new token.
	// OAuth tokens are baked into the sandbox env at session-creation time and
	// cannot be injected into a running process; closing the runner forces a
	// clean restart with fresh credentials on the next chat turn.
	_ = s.InvalidateUser(flow.UserID)
	return nil
}

// Disconnect removes the OAuth bundle for the given provider and user.
func (s *Service) Disconnect(ctx context.Context, userID string, provider string) error {
	if s.vaultSvc == nil {
		return fmt.Errorf("vault not configured")
	}
	if s.registry == nil {
		return fmt.Errorf("provider registry not set")
	}
	if err := s.registry.DeleteBundle(ctx, s.vaultSvc, provider, userID); err != nil {
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
	outcome := ""
	if fs.State == oauth.FlowStatePending || fs.State == oauth.FlowStateCompleting {
		outcome = OAuthOutcomeUserConsentRequired
	}
	return FlowStatus{
		Provider:        string(fs.Provider),
		FlowID:          fs.FlowID,
		VerificationURI: fs.VerificationURI,
		UserCode:        fs.UserCode,
		ExpiresAt:       fs.ExpiresAt,
		State:           string(fs.State),
		Outcome:         outcome,
		Error:           fs.Error,
		RequestedScopes: append([]string(nil), fs.DesiredScopes...),
	}
}
