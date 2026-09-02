package mcp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

// StartOAuth runs discovery, resolves (or registers) the client, persists a
// one-shot flow row, and returns the authorization URL the user's browser must
// open. The flow binds the initiating user, so the unauthenticated callback can
// re-identify safely.
func (s *Service) StartOAuth(ctx context.Context, reg Registration, userID, callback string) (string, string, time.Time, error) {
	if reg.Transport != TransportStreamableHTTP {
		return "", "", time.Time{}, fmt.Errorf("mcp: auth_type %q requires the streamable_http transport", AuthTypeOAuth)
	}
	challenges, err := fetchChallenge(ctx, reg, s.endpoints)
	if err != nil {
		return "", "", time.Time{}, err
	}
	prm, err := protectedResourceMetadata(ctx, reg, challenges, s.endpoints)
	if err != nil {
		return "", "", time.Time{}, err
	}
	asm, err := authServerMetadata(ctx, prm.AuthorizationServers[0], s.endpoints)
	if err != nil {
		return "", "", time.Time{}, err
	}
	clientID, clientSecret, err := s.resolveOAuthClient(ctx, reg, asm, callback)
	if err != nil {
		return "", "", time.Time{}, err
	}

	scopes := scopesFromChallenges(challenges)
	if len(scopes) == 0 {
		scopes = prm.ScopesSupported
	}
	verifier := oauth2.GenerateVerifier()
	flowID := uuid.Must(uuid.NewV7()).String()
	expiresAt := time.Now().UTC().Add(oauthFlowTTL)
	secretRef := ""
	if clientSecret != "" {
		secretRef = oauthClientSecretName(reg.ID)
	}
	configRaw, err := oauthFlowConfig{
		ClientID: clientID, ClientSecretRef: secretRef,
		TokenEndpoint: asm.TokenEndpoint, AuthStyle: int(oauth2.AuthStyleInParams),
		Resource: prm.Resource, Scopes: scopes, RedirectURI: callback,
	}.marshal()
	if err != nil {
		return "", "", time.Time{}, err
	}
	if _, err := s.db.CreateMCPOAuthFlow(ctx, flowParams(flowID, reg, userID, verifier, configRaw, expiresAt)); err != nil {
		return "", "", time.Time{}, fmt.Errorf("mcp: persist oauth flow: %w", err)
	}
	authURL := (&oauth2.Config{
		ClientID: clientID, ClientSecret: clientSecret,
		Endpoint:    oauth2.Endpoint{AuthURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint},
		RedirectURL: callback, Scopes: scopes,
	}).AuthCodeURL(flowID,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("resource", prm.Resource))
	return authURL, flowID, expiresAt, nil
}

// CompleteOAuth consumes the flow exactly once, exchanges the code with the
// stored verifier, writes the bundle to the flow's credential tuple, resets the
// status, and re-probes with the fresh credential.
func (s *Service) CompleteOAuth(ctx context.Context, flowID, code string) (Registration, error) {
	flow, err := s.db.ConsumeMCPOAuthFlow(ctx, flowID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Registration{}, fmt.Errorf("mcp: oauth flow is unknown, expired, or already used")
	}
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: consume oauth flow: %w", err)
	}
	cfg, err := decodeOAuthFlowConfig(flow.OauthConfig)
	if err != nil {
		return Registration{}, err
	}
	owner := CredentialOwner{Scope: flow.CredentialScope, UserID: textOrEmpty(flow.CredentialUserID), AgentID: textOrEmpty(flow.CredentialAgentID)}

	row, err := s.db.GetMCPServerByID(ctx, flow.ServerID)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: get registration: %w", err)
	}
	reg := registrationFromRow(row)

	// Same SSRF-safe client binding as the refresh path.
	exchangeCtx, cancel := context.WithTimeout(oauth2Context(s.endpoints), oauthExchangeTimeout)
	defer cancel()
	clientSecret := ""
	if cfg.ClientSecretRef != "" {
		clientSecret = s.oauthClientSecret(ctx, reg)
	}
	tok, err := (&oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: clientSecret,
		Endpoint:    oauth2.Endpoint{TokenURL: cfg.TokenEndpoint, AuthStyle: oauth2.AuthStyle(cfg.AuthStyle)},
		RedirectURL: cfg.RedirectURI, Scopes: cfg.Scopes,
	}).Exchange(exchangeCtx, code,
		oauth2.VerifierOption(flow.PkceVerifier),
		oauth2.SetAuthURLParam("resource", cfg.Resource))
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: exchange authorization code: %w", err)
	}

	bundle := OAuthBundle{
		Version: 1, ClientID: cfg.ClientID, TokenEndpoint: cfg.TokenEndpoint,
		AuthStyle: cfg.AuthStyle, Resource: cfg.Resource,
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken,
		AccessExpiresAt: tok.Expiry, GrantedScope: tokenScope(tok),
	}
	if err := s.storeBundle(ctx, reg, owner, bundle); err != nil {
		return Registration{}, err
	}
	if err := s.SetStatus(ctx, reg.ID, StatusUnknown, ""); err != nil {
		return Registration{}, err
	}
	probed, err := s.Probe(ctx, reg, owner)
	if err != nil {
		return Registration{}, err
	}
	return probed, nil
}

// Disconnect removes the caller-appropriate bundle and marks the server
// needs_auth, so subsequent tool calls fail closed until a reconnect.
func (s *Service) Disconnect(ctx context.Context, reg Registration, userID string) (Registration, error) {
	owner := s.CredentialOwner(reg, userID)
	if s.vault != nil {
		if err := s.deleteToken(ctx, owner.Scope, owner.UserID, owner.AgentID, oauthBundleName(reg.ID)); err != nil {
			return Registration{}, fmt.Errorf("mcp: delete oauth bundle: %w", err)
		}
	}
	if err := s.SetStatus(ctx, reg.ID, StatusNeedsAuth, credentialRejectedHint); err != nil {
		return Registration{}, err
	}
	return s.GetMCPServerForOwner(ctx, reg.ID)
}

// GetMCPServerForOwner re-reads a registration by id, unmapped by scope —
// the callers have already passed the PEP for this exact row.
func (s *Service) GetMCPServerForOwner(ctx context.Context, id string) (Registration, error) {
	row, err := s.db.GetMCPServerByID(ctx, id)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: get registration: %w", err)
	}
	return registrationFromRow(row), nil
}

// HasUserCredential reports whether the given user has a credential to use
// for this registration. per_user registrations need their own bundle; shared
// registrations answer for every user. False for per_user means the tools
// list should show mcp_needs_auth for exactly this user.
func (s *Service) HasUserCredential(ctx context.Context, reg Registration, userID string) bool {
	if reg.CredentialMode != CredentialModePerUser {
		return true
	}
	bundle, err := s.loadBundle(ctx, reg, s.CredentialOwner(reg, userID))
	return err == nil && bundle != nil && bundle.AccessToken != ""
}

// OAuthState is the user-facing OAuth view of one registration, computed for
// the calling user (per_user) or the registration owner (shared). No token
// material ever reaches it.
type OAuthState struct {
	Connected        bool
	AccessExpiresAt  time.Time
	NeedsReconnect   bool
	ClientRegistered bool
}

// OAuthState resolves the API projection for one registration and user.
func (s *Service) OAuthState(ctx context.Context, reg Registration, userID string) OAuthState {
	state := OAuthState{ClientRegistered: reg.OAuthClientID != ""}
	if reg.AuthType != AuthTypeOAuth {
		return state
	}
	bundle, err := s.loadBundle(ctx, reg, s.CredentialOwner(reg, userID))
	if err != nil || bundle == nil || bundle.AccessToken == "" {
		return state
	}
	state.Connected = true
	state.AccessExpiresAt = bundle.AccessExpiresAt
	state.NeedsReconnect = bundle.RefreshToken == "" && time.Now().After(bundle.AccessExpiresAt)
	return state
}
