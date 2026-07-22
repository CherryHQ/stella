package webhook

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/credential"
)

type Service struct {
	store  Store
	users  UserState
	access OwnerAgentAccess
	cipher SecretCipher
}

type Config struct {
	Store  Store
	Users  UserState
	Access OwnerAgentAccess
	Cipher SecretCipher
}

func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Users == nil || cfg.Access == nil {
		return nil, errors.New("webhook: store, user state, and owner-agent access are required")
	}
	return &Service{store: cfg.Store, users: cfg.Users, access: cfg.Access, cipher: cfg.Cipher}, nil
}

// Issue locks the current channel binding before it validates the owner and
// inserts the endpoint, so a concurrent rebind cannot slip between those facts.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	if req.ChannelID == "" {
		return IssueResult{}, ErrInvalidChannelID
	}
	if req.OwnerUserID == "" {
		return IssueResult{}, ErrInvalidOwnerUserID
	}
	if !req.Provider.Valid() {
		return IssueResult{}, ErrInvalidProvider
	}
	if req.Provider == ProviderGitHub && !req.GitHub.Valid() {
		return IssueResult{}, ErrInvalidGitHubPolicy
	}

	var capability, githubSecret string
	rec, err := s.store.BindEndpoint(ctx, req.ChannelID, func(ctx context.Context, agentID string) (endpointRecord, error) {
		active, err := s.users.IsActive(ctx, req.OwnerUserID)
		if err != nil {
			return endpointRecord{}, fmt.Errorf("webhook: get owner state: %w", err)
		}
		if !active {
			return endpointRecord{}, ErrOwnerInactive
		}
		allowed, err := s.access.CanUseOwner(ctx, req.OwnerUserID, agentID)
		if err != nil {
			return endpointRecord{}, fmt.Errorf("webhook: authorize endpoint owner: %w", err)
		}
		if !allowed {
			return endpointRecord{}, ErrOwnerAgentForbidden
		}
		rec, secret, token, err := s.newCredential(req.Provider)
		if err != nil {
			return endpointRecord{}, err
		}
		rec.ID = uuid.Must(uuid.NewV7()).String()
		rec.OwnerUserID = req.OwnerUserID
		rec.Provider = req.Provider
		capability, githubSecret = token, secret
		return rec, nil
	})
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Endpoint: rec.Endpoint, Capability: capability, GitHubWebhookSecret: githubSecret}, nil
}

func (s *Service) GetByChannel(ctx context.Context, channelID string) (Endpoint, error) {
	rec, err := s.store.GetEndpointByChannel(ctx, channelID)
	if err != nil {
		return Endpoint{}, err
	}
	return rec.Endpoint, nil
}

// Rotate changes only credentials. Provider and owner binding are immutable;
// callers must revoke and issue a new endpoint to change either identity.
func (s *Service) Rotate(ctx context.Context, endpointID string) (RotationResult, error) {
	rec, err := s.store.GetEndpoint(ctx, endpointID)
	if err != nil {
		return RotationResult{}, err
	}
	if !rec.Provider.Valid() {
		return RotationResult{}, ErrInvalidProvider
	}
	return s.rotate(ctx, rec)
}

func (s *Service) Delete(ctx context.Context, endpointID string) (bool, error) {
	n, err := s.store.DeleteEndpoint(ctx, endpointID)
	return n == 1, err
}

func (s *Service) rotate(ctx context.Context, current endpointRecord) (RotationResult, error) {
	next, secret, capability, err := s.newCredential(current.Provider)
	if err != nil {
		return RotationResult{}, err
	}
	next.Endpoint = current.Endpoint
	next.ID = current.ID
	next.Provider = current.Provider
	next, err = s.store.RotateEndpoint(ctx, next)
	if err != nil {
		return RotationResult{}, fmt.Errorf("webhook: rotate endpoint: %w", err)
	}
	return RotationResult{Endpoint: next.Endpoint, Capability: capability, GitHubWebhookSecret: secret}, nil
}

func (s *Service) newCredential(provider Provider) (endpointRecord, string, string, error) {
	minted, err := credential.MintOpaqueWithPrefix(TokenPrefix)
	if err != nil {
		return endpointRecord{}, "", "", fmt.Errorf("webhook: mint capability: %w", err)
	}
	rec := endpointRecord{TokenPublicID: minted.PublicID, TokenHash: minted.TokenHash}
	rec.TokenLast4 = minted.Last4
	if provider != ProviderGitHub {
		return rec, "", minted.Plaintext, nil
	}
	if s.cipher == nil {
		return endpointRecord{}, "", "", errors.New("webhook: github endpoint requires a system secret cipher")
	}
	secret, err := randomSecret()
	if err != nil {
		return endpointRecord{}, "", "", err
	}
	ciphertext, err := s.cipher.EncryptSystem(secret)
	if err != nil {
		return endpointRecord{}, "", "", fmt.Errorf("webhook: encrypt github secret: %w", err)
	}
	rec.ProviderSecretCiphertext = ciphertext
	return rec, secret, minted.Plaintext, nil
}

func randomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("webhook: generate github secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// ResolveCapability does one indexed lookup then constant-time verifier check.
// All malformed, mismatched, missing, or inactive cases collapse to ErrNotFound.
func (s *Service) ResolveCapability(ctx context.Context, raw string) (Invocation, error) {
	if !strings.HasPrefix(raw, TokenPrefix) {
		return Invocation{}, ErrNotFound
	}
	publicID, secret, err := credential.ParseOpaqueToken(TokenPrefix, raw)
	if err != nil {
		return Invocation{}, ErrNotFound
	}
	rec, err := s.store.ResolveEndpoint(ctx, publicID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Invocation{}, ErrNotFound
		}
		return Invocation{}, fmt.Errorf("webhook: resolve endpoint: %w", err)
	}
	if !rec.Provider.Valid() || !rec.ChannelEnabled || !rec.OwnerActive || !rec.AgentEnabled || rec.AgentID == "" ||
		!matchesTokenHash(secret, rec.TokenHash) {
		return Invocation{}, ErrNotFound
	}
	authority, err := agentaccess.WorkerAgentAuthority(rec.OwnerUserID, rec.AgentID)
	if err != nil {
		return Invocation{}, ErrNotFound
	}
	invocation := Invocation{Endpoint: rec.Endpoint, AgentID: rec.AgentID, Authority: authority}
	if rec.Provider == ProviderGitHub {
		if s.cipher == nil {
			return Invocation{}, fmt.Errorf("webhook: github endpoint cipher unavailable")
		}
		secret, err := s.cipher.DecryptSystem(rec.ProviderSecretCiphertext)
		if err != nil {
			return Invocation{}, fmt.Errorf("webhook: decrypt github secret: %w", err)
		}
		if secret == "" {
			return Invocation{}, errors.New("webhook: decrypted github secret is empty")
		}
		invocation.githubSecret = secret
	}
	return invocation, nil
}

func matchesTokenHash(secret, tokenHash string) bool {
	return subtle.ConstantTimeCompare([]byte(credential.HashSecret(secret)), []byte(tokenHash)) == 1
}

func (p GitHubPolicy) Valid() bool {
	return nonEmptyUnique(p.Events) && nonEmptyUnique(p.Repositories)
}

func nonEmptyUnique(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || len(value) > deliveryIDMaxLen {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}
