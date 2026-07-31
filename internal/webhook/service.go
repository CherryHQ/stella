package webhook

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/credential"
)

// Service is the authoritative webhook endpoint domain. It owns credential
// minting, issuance validation, CAS rotation, capability resolution, and the
// admission seam.
//
// lifecycle is a single process-global RW mutex sufficient for the supported
// single-replica topology. Issue/Rotate/Delete take the write lock through their
// database mutation; final Admit takes the read lock through final revalidation
// and the ChatAdmitted callback only, so a mutation that wins first invalidates
// every later admission and an admission that wins first blocks the mutation
// only until the turn is admitted (not completed).
//
//	// Global lifecycle lock is sufficient for the supported single-replica topology.
//	// Replace with per-endpoint fencing if measured contention or multi-replica support arrives.
type Service struct {
	store     Store
	users     UserState
	access    OwnerAgentAccess
	lifecycle sync.RWMutex
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
// It observes the binding, runs owner active-state and Agent-use prechecks
// entirely outside any transaction, then takes the short channel-row lock only
// to re-verify the exact observed binding and insert — so no user/access lookup
// ever runs while the row lock (or the lifecycle write lock) is held. A binding
// that changed between observe and lock returns a retryable conflict. Provider
// and owner are immutable once issued; changing either requires revoke then
// re-issue.
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

	// 1. Observe the current binding without a transaction.
	observed, err := s.store.ObserveBinding(ctx, req.ChannelID)
	if err != nil {
		return IssueResult{}, err
	}
	if observed.Type != "webhook" {
		return IssueResult{}, ErrChannelNotWebhook
	}
	if observed.AgentID == "" {
		return IssueResult{}, ErrNotFound
	}
	if !observed.AgentEnabled {
		return IssueResult{}, ErrAgentDisabled
	}

	// 2. Owner active-state and Agent-use prechecks, outside any transaction.
	active, err := s.users.IsActive(ctx, req.OwnerUserID)
	if err != nil {
		return IssueResult{}, fmt.Errorf("webhook: get owner state: %w", err)
	}
	if !active {
		return IssueResult{}, ErrOwnerInactive
	}
	allowed, err := s.access.CanUseOwner(ctx, req.OwnerUserID, observed.AgentID)
	if err != nil {
		return IssueResult{}, fmt.Errorf("webhook: authorize endpoint owner: %w", err)
	}
	if !allowed {
		return IssueResult{}, ErrOwnerAgentForbidden
	}

	// 3. Under the lifecycle write lock, take the short channel-row lock, re-verify
	// the exact observed binding, and insert. The locked callback performs no
	// external or authorization lookup.
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()

	var capability string
	rec, err := s.store.BindEndpoint(ctx, req.ChannelID, func(_ context.Context, binding ChannelBinding) (endpointRecord, error) {
		if binding.Type != "webhook" || binding.AgentID != observed.AgentID || !binding.AgentEnabled {
			return endpointRecord{}, ErrChannelBindingChanged
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
	s.lifecycle.Lock()
	updated, err := s.store.RotateEndpoint(ctx, channelID, expectedETag, next)
	s.lifecycle.Unlock()
	if err != nil {
		return RotationResult{}, err
	}
	return RotationResult{Endpoint: updated.Endpoint, Capability: capability}, nil
}

// Delete revokes the endpoint. It reports whether a row was removed so callers
// can distinguish an already-absent endpoint.
func (s *Service) Delete(ctx context.Context, channelID string) (bool, error) {
	s.lifecycle.Lock()
	n, err := s.store.DeleteEndpoint(ctx, channelID)
	s.lifecycle.Unlock()
	return n == 1, err
}

// ResolveCandidate is the first admission stage: it parses and verifies the
// opaque capability, returning a non-loggable Candidate plus the safe endpoint
// id for pre-read limiting. It holds no lifecycle lock, so the caller may read a
// request body between this and Admit. Every malformed/mismatched/missing case
// collapses to ErrNotFound.
func (s *Service) ResolveCandidate(ctx context.Context, raw string) (Candidate, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return Candidate{}, ErrNotFound
	}
	publicID, secret, err := credential.ParseOpaqueToken(TokenPrefix, raw)
	if err != nil {
		return Candidate{}, ErrNotFound
	}
	rec, err := s.store.ResolveByPublicID(ctx, publicID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Candidate{}, ErrNotFound
		}
		return Candidate{}, fmt.Errorf("webhook: resolve candidate: %w", err)
	}
	if !matchesTokenHash(secret, rec.TokenHash) {
		return Candidate{}, ErrNotFound
	}
	return Candidate{EndpointID: rec.ChannelID, publicID: publicID, secret: secret}, nil
}

// Admit is the second admission stage. Under the lifecycle read lock it
// re-resolves the current endpoint by the candidate's credential, revalidates
// the constant-time verifier, the durable active state of owner/channel/Agent,
// and the owner's current Agent-use permission through the PEP, then reconstructs
// the fixed worker authority and invokes callback. The read lock is held only
// through this revalidation and the callback's synchronous admission (the
// callback returns at admission, not completion) and released immediately after.
//
// A rotate or revoke that committed after ResolveCandidate makes the credential
// unresolvable here, so callback is never invoked. The re-resolve is a single
// query whose connection is released before callback runs, so no database
// transaction or row lock spans callback.
func (s *Service) Admit(ctx context.Context, cand Candidate, callback AdmitCallback) error {
	if callback == nil {
		return errors.New("webhook: admit callback is required")
	}
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()

	rec, err := s.store.ResolveEndpoint(ctx, cand.publicID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ErrNotFound
		}
		return fmt.Errorf("webhook: admit resolve: %w", err)
	}
	if !rec.Provider.Valid() || rec.AgentID == "" || !rec.ChannelEnabled || !rec.OwnerActive || !rec.AgentEnabled {
		return ErrNotFound
	}
	if !matchesTokenHash(cand.secret, rec.TokenHash) {
		return ErrNotFound
	}
	// The endpoint fixes identity; it does not freeze permission. Recheck the
	// owner's current Agent-use permission through the existing PEP so a withdrawn
	// assignment fails closed on the next admission.
	allowed, err := s.access.CanUseOwner(ctx, rec.OwnerUserID, rec.AgentID)
	if err != nil {
		return fmt.Errorf("webhook: admit authorize: %w", err)
	}
	if !allowed {
		return ErrOwnerAgentForbidden
	}
	authority, err := agentaccess.WorkerAgentAuthority(rec.OwnerUserID, rec.AgentID)
	if err != nil {
		return ErrNotFound
	}
	return callback(ctx, AdmittedInvocation{
		ChannelID:   rec.ChannelID,
		OwnerUserID: rec.OwnerUserID,
		AgentID:     rec.AgentID,
		Provider:    rec.Provider,
		Authority:   authority,
	})
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
