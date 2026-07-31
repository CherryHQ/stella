package webhook

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/credential"
)

// Service is the authoritative webhook endpoint domain. It owns credential
// minting, issuance validation, CAS rotation, and capability resolution.
type Service struct {
	store  Store
	users  UserState
	access OwnerAgentAccess
}

type Config struct {
	Store  Store
	Users  UserState
	Access OwnerAgentAccess
}

func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Users == nil || cfg.Access == nil {
		return nil, errors.New("webhook: store, user state, and owner-agent access are required")
	}
	return &Service{store: cfg.Store, users: cfg.Users, access: cfg.Access}, nil
}

// Issue binds an owner to the channel's current Agent and mints a capability.
// The channel row is locked before the binding is re-verified and the endpoint
// inserted, so a concurrent rebind cannot slip between the observed and
// persisted facts. Provider and owner are immutable once issued; changing
// either requires revoke then re-issue.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	if req.ChannelID == "" {
		return IssueResult{}, ErrInvalidChannelID
	}
	if uuid.Validate(req.OwnerUserID) != nil {
		return IssueResult{}, ErrInvalidOwnerUserID
	}
	if !req.Provider.Valid() {
		return IssueResult{}, ErrInvalidProvider
	}

	var capability string
	rec, err := s.store.BindEndpoint(ctx, req.ChannelID, func(ctx context.Context, binding ChannelBinding) (endpointRecord, error) {
		if binding.Type != "webhook" {
			return endpointRecord{}, ErrChannelNotWebhook
		}
		if binding.AgentID == "" {
			return endpointRecord{}, ErrNotFound
		}
		if !binding.AgentEnabled {
			return endpointRecord{}, ErrAgentDisabled
		}
		active, err := s.users.IsActive(ctx, req.OwnerUserID)
		if err != nil {
			return endpointRecord{}, fmt.Errorf("webhook: get owner state: %w", err)
		}
		if !active {
			return endpointRecord{}, ErrOwnerInactive
		}
		allowed, err := s.access.CanUseOwner(ctx, req.OwnerUserID, binding.AgentID)
		if err != nil {
			return endpointRecord{}, fmt.Errorf("webhook: authorize endpoint owner: %w", err)
		}
		if !allowed {
			return endpointRecord{}, ErrOwnerAgentForbidden
		}
		rec, token, err := s.newCredential()
		if err != nil {
			return endpointRecord{}, err
		}
		rec.OwnerUserID = req.OwnerUserID
		rec.Provider = req.Provider
		capability = token
		return rec, nil
	})
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Endpoint: rec.Endpoint, Capability: capability}, nil
}

// GetByChannel returns secret-safe endpoint metadata for a channel.
func (s *Service) GetByChannel(ctx context.Context, channelID string) (Endpoint, error) {
	rec, err := s.store.GetEndpointByChannel(ctx, channelID)
	if err != nil {
		return Endpoint{}, err
	}
	return rec.Endpoint, nil
}

// Rotate replaces the endpoint's capability if expectedETag still matches the
// current row's opaque etag (bound to token_public_id + revision). Two clients
// rotating the same observed endpoint cannot both succeed: the loser sees
// ErrStaleETag. A stale etag from a revoked+recreated endpoint or a different
// channel cannot match either, because token_public_id is unique per credential.
// Provider and owner are unchanged.
func (s *Service) Rotate(ctx context.Context, channelID string, expectedETag string) (RotationResult, error) {
	if channelID == "" {
		return RotationResult{}, ErrInvalidChannelID
	}
	if expectedETag == "" {
		return RotationResult{}, ErrInvalidETag
	}
	next, capability, err := s.newCredential()
	if err != nil {
		return RotationResult{}, err
	}
	updated, err := s.store.RotateEndpoint(ctx, channelID, expectedETag, next)
	if err != nil {
		return RotationResult{}, err
	}
	return RotationResult{Endpoint: updated.Endpoint, Capability: capability}, nil
}

// Delete revokes the endpoint. It reports whether a row was removed so callers
// can distinguish an already-absent endpoint.
func (s *Service) Delete(ctx context.Context, channelID string) (bool, error) {
	n, err := s.store.DeleteEndpoint(ctx, channelID)
	return n == 1, err
}

// Resolve maps a presented capability token to its endpoint. It performs one
// indexed lookup then a constant-time verifier comparison; every malformed,
// mismatched, or missing case collapses to ErrNotFound. Runtime admission
// (owner/channel/Agent revalidation) is layered on top of this in a later phase.
func (s *Service) Resolve(ctx context.Context, raw string) (Endpoint, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return Endpoint{}, ErrNotFound
	}
	publicID, secret, err := credential.ParseOpaqueToken(TokenPrefix, raw)
	if err != nil {
		return Endpoint{}, ErrNotFound
	}
	rec, err := s.store.ResolveByPublicID(ctx, publicID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Endpoint{}, ErrNotFound
		}
		return Endpoint{}, fmt.Errorf("webhook: resolve endpoint: %w", err)
	}
	if !matchesTokenHash(secret, rec.TokenHash) {
		return Endpoint{}, ErrNotFound
	}
	return rec.Endpoint, nil
}

func (s *Service) newCredential() (endpointRecord, string, error) {
	minted, err := credential.MintOpaqueWithPrefix(TokenPrefix)
	if err != nil {
		return endpointRecord{}, "", fmt.Errorf("webhook: mint capability: %w", err)
	}
	return endpointRecord{
		Endpoint:  Endpoint{TokenPublicID: minted.PublicID, TokenLast4: minted.Last4},
		TokenHash: minted.TokenHash,
	}, minted.Plaintext, nil
}

func matchesTokenHash(secret, tokenHash string) bool {
	return subtle.ConstantTimeCompare([]byte(credential.HashSecret(secret)), []byte(tokenHash)) == 1
}
