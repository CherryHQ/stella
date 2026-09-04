package mcp

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"

	credoauth "github.com/CherryHQ/stella/internal/connections/oauth"
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
	clientID, _, err := s.resolveOAuthClient(ctx, reg, asm, callback)
	if err != nil {
		return "", "", time.Time{}, err
	}

	scopes := scopesFromChallenges(challenges)
	if len(scopes) == 0 {
		scopes = prm.ScopesSupported
	}
	if s.oauthBroker == nil {
		return "", "", time.Time{}, fmt.Errorf("mcp: oauth requires the vault and durable flow store")
	}
	owner := credentialOwnerFor(reg, userID)
	flow, authURL, err := s.oauthBroker.Start(ctx, credoauth.AuthCodeStart{
		ProviderKey: "mcp:" + reg.ID, TargetKind: "mcp", TargetID: reg.ID, ServerID: reg.ID,
		UserID: userID, Owner: credoauth.CredentialOwner{Scope: owner.Scope, UserID: owner.UserID, AgentID: owner.AgentID},
		BundleName: oauthBundleName(reg.ID), TTL: oauthFlowTTL,
		Config: credoauth.AuthCodeConfig{
			ClientID:         clientID,
			AuthorizationURL: asm.AuthorizationEndpoint, TokenURL: asm.TokenEndpoint,
			AuthStyle: int(oauth2.AuthStyleInParams), Resource: prm.Resource, Scopes: scopes, RedirectURI: callback,
		},
	})
	if err != nil {
		return "", "", time.Time{}, err
	}
	return authURL, flow.ID, flow.ExpiresAt, nil
}

// CompleteOAuth consumes the flow exactly once, exchanges the code with the
// stored verifier, writes the bundle to the flow's credential tuple, resets the
// status, and re-probes with the fresh credential.
func (s *Service) CompleteOAuth(ctx context.Context, flowID, code string) (Registration, error) {
	if s.oauthBroker == nil {
		return Registration{}, fmt.Errorf("mcp: oauth requires the vault and durable flow store")
	}
	flow, err := s.oauthBroker.Get(ctx, flowID)
	if err != nil || flow.TargetKind != "mcp" || flow.TargetID == "" {
		return Registration{}, fmt.Errorf("mcp: oauth flow is unknown, expired, or already used")
	}
	row, err := s.db.GetMCPServerByID(ctx, flow.TargetID)
	if err != nil {
		return Registration{}, fmt.Errorf("mcp: get registration: %w", err)
	}
	reg := registrationFromRow(row)

	exchangeCtx, cancel := context.WithTimeout(ctx, oauthExchangeTimeout)
	defer cancel()
	if _, _, err := s.oauthBroker.Complete(exchangeCtx, flowID, code, s.oauthClientSecret(ctx, reg), oauthHTTPClient(s.endpoints)); err != nil {
		return Registration{}, fmt.Errorf("mcp: %w", err)
	}
	if err := s.SetStatus(ctx, reg.ID, StatusUnknown, ""); err != nil {
		return Registration{}, err
	}
	owner := CredentialOwner{Scope: flow.Owner.Scope, UserID: flow.Owner.UserID, AgentID: flow.Owner.AgentID}
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
		if err := s.oauthTokens.Delete(ctx, oauthBundleRef(reg, owner)); err != nil {
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
	if s.oauthTokens == nil {
		return false
	}
	bundle, err := s.oauthTokens.Load(ctx, oauthBundleRef(reg, s.CredentialOwner(reg, userID)))
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
	if s.oauthTokens == nil {
		return state
	}
	bundle, err := s.oauthTokens.Load(ctx, oauthBundleRef(reg, s.CredentialOwner(reg, userID)))
	if err != nil || bundle == nil || bundle.AccessToken == "" {
		return state
	}
	state.Connected = true
	state.AccessExpiresAt = bundle.AccessExpiresAt
	state.NeedsReconnect = bundle.RefreshToken == "" && time.Now().After(bundle.AccessExpiresAt)
	return state
}
