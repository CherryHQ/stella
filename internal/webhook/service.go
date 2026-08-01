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

// Service is the deep webhook resource boundary. A single lifecycle fence is
// sufficient for Stella's supported single-replica deployment. Upgrade to a
// distributed/per-webhook fence when multi-replica ingress is supported.
type Service struct {
	store     Store
	users     UserState
	access    UserAgentAccess
	lifecycle sync.RWMutex
}

type Config struct {
	Store  Store
	Users  UserState
	Access UserAgentAccess
}

func NewService(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Users == nil || cfg.Access == nil {
		return nil, errors.New("webhook: store, user state, and user-agent access are required")
	}
	return &Service{store: cfg.Store, users: cfg.Users, access: cfg.Access}, nil
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (IssueResult, error) {
	if err := validateCreate(req); err != nil {
		return IssueResult{}, err
	}
	if err := s.ValidateUserAgent(ctx, req.UserID, req.AgentID); err != nil {
		return IssueResult{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return IssueResult{}, fmt.Errorf("webhook: mint id: %w", err)
	}
	rec, capability, err := newCredential()
	if err != nil {
		return IssueResult{}, err
	}
	rec.ID, rec.UserID, rec.Name, rec.AgentID = id.String(), req.UserID, strings.TrimSpace(req.Name), req.AgentID
	rec.Provider, rec.IsEnabled = req.Provider, req.IsEnabled
	rec.WaitTimeoutSeconds, rec.MaxRunTimeoutSeconds = req.WaitTimeoutSeconds, req.MaxRunTimeoutSeconds
	s.lifecycle.Lock()
	stored, err := s.store.Create(ctx, rec)
	s.lifecycle.Unlock()
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Webhook: stored.Webhook, Capability: capability}, nil
}

func (s *Service) Get(ctx context.Context, userID, id string) (Webhook, error) {
	if uuid.Validate(userID) != nil || uuid.Validate(id) != nil {
		return Webhook{}, ErrNotFound
	}
	rec, err := s.store.Get(ctx, id, userID)
	return rec.Webhook, err
}

func (s *Service) List(ctx context.Context, userID string, limit, offset int32) ([]Webhook, error) {
	if uuid.Validate(userID) != nil {
		return nil, ErrNotFound
	}
	records, err := s.store.List(ctx, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	out := make([]Webhook, len(records))
	for i := range records {
		out[i] = records[i].Webhook
	}
	return out, nil
}

func (s *Service) Update(ctx context.Context, req UpdateRequest) (Webhook, error) {
	if uuid.Validate(req.UserID) != nil {
		return Webhook{}, ErrInvalidUserID
	}
	if uuid.Validate(req.ID) != nil {
		return Webhook{}, ErrInvalidID
	}
	if _, err := s.store.Get(ctx, req.ID, req.UserID); err != nil {
		return Webhook{}, err
	}
	if err := validateUpdate(req); err != nil {
		return Webhook{}, err
	}
	if req.AgentID != nil {
		if err := s.ValidateUserAgent(ctx, req.UserID, *req.AgentID); err != nil {
			return Webhook{}, err
		}
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	stored, err := s.store.Update(ctx, req)
	if err != nil {
		return Webhook{}, err
	}
	return stored.Webhook, nil
}

func (s *Service) Rotate(ctx context.Context, userID, id, etag string) (IssueResult, error) {
	if uuid.Validate(userID) != nil || uuid.Validate(id) != nil {
		return IssueResult{}, ErrNotFound
	}
	if etag == "" {
		return IssueResult{}, ErrInvalidETag
	}
	next, capability, err := newCredential()
	if err != nil {
		return IssueResult{}, err
	}
	s.lifecycle.Lock()
	stored, err := s.store.Rotate(ctx, id, userID, etag, next)
	s.lifecycle.Unlock()
	if err != nil {
		return IssueResult{}, err
	}
	return IssueResult{Webhook: stored.Webhook, Capability: capability}, nil
}

func (s *Service) Delete(ctx context.Context, userID, id string) (bool, error) {
	if uuid.Validate(userID) != nil || uuid.Validate(id) != nil {
		return false, ErrNotFound
	}
	s.lifecycle.Lock()
	n, err := s.store.Delete(ctx, id, userID)
	s.lifecycle.Unlock()
	return n == 1, err
}

func (s *Service) ValidateUserAgent(ctx context.Context, userID, agentID string) error {
	if uuid.Validate(userID) != nil {
		return ErrInvalidUserID
	}
	if strings.TrimSpace(agentID) == "" {
		return ErrInvalidAgentID
	}
	active, err := s.users.IsActive(ctx, userID)
	if err != nil {
		return fmt.Errorf("webhook: get user state: %w", err)
	}
	if !active {
		return ErrUserInactive
	}
	allowed, err := s.access.CanUseUser(ctx, userID, agentID)
	if err != nil {
		return fmt.Errorf("webhook: authorize user agent: %w", err)
	}
	if !allowed {
		return ErrUserAgentForbidden
	}
	return nil
}

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
		return Candidate{}, hideNotFound(err)
	}
	if !matchesTokenHash(secret, rec.TokenHash) {
		return Candidate{}, ErrNotFound
	}
	return Candidate{WebhookID: rec.ID, publicID: publicID, secret: secret}, nil
}

func (s *Service) Admit(ctx context.Context, cand Candidate, callback AdmitCallback) error {
	if callback == nil {
		return errors.New("webhook: admit callback is required")
	}
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	// User/Agent active state and user→Agent permission are point-in-time checks.
	// Their independent management paths do not share this webhook lifecycle
	// fence: an admission whose checks began before a concurrent withdrawal may
	// finish, while every request whose checks begin afterward fails closed.
	rec, err := s.store.ResolveAdmitted(ctx, cand.publicID)
	if err != nil {
		return hideNotFound(err)
	}
	if !rec.Provider.Valid() || !matchesTokenHash(cand.secret, rec.TokenHash) {
		return ErrNotFound
	}
	allowed, err := s.access.CanUseUser(ctx, rec.UserID, rec.AgentID)
	if err != nil {
		return fmt.Errorf("webhook: admit authorize: %w", err)
	}
	if !allowed {
		return ErrUserAgentForbidden
	}
	authority, err := agentaccess.WorkerAgentAuthority(rec.UserID, rec.AgentID)
	if err != nil {
		return ErrNotFound
	}
	return callback(ctx, AdmittedInvocation{WebhookID: rec.ID, UserID: rec.UserID, AgentID: rec.AgentID, Provider: rec.Provider, WaitTimeoutSeconds: rec.WaitTimeoutSeconds, MaxRunTimeoutSeconds: rec.MaxRunTimeoutSeconds, Authority: authority})
}

func validateCreate(req CreateRequest) error {
	if uuid.Validate(req.UserID) != nil {
		return ErrInvalidUserID
	}
	return validateWebhook(Webhook{Name: req.Name, AgentID: req.AgentID, Provider: req.Provider, WaitTimeoutSeconds: req.WaitTimeoutSeconds, MaxRunTimeoutSeconds: req.MaxRunTimeoutSeconds})
}

func validateUpdate(req UpdateRequest) error {
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		return ErrInvalidName
	}
	if req.AgentID != nil && strings.TrimSpace(*req.AgentID) == "" {
		return ErrInvalidAgentID
	}
	if req.WaitTimeoutSeconds != nil && (*req.WaitTimeoutSeconds < 1 || *req.WaitTimeoutSeconds > WaitTimeoutCeilingSeconds) {
		return ErrInvalidTimeout
	}
	if req.MaxRunTimeoutSeconds != nil && (*req.MaxRunTimeoutSeconds < 1 || *req.MaxRunTimeoutSeconds > RunTimeoutCeilingSeconds) {
		return ErrInvalidTimeout
	}
	return nil
}

func validateWebhook(w Webhook) error {
	if strings.TrimSpace(w.Name) == "" {
		return ErrInvalidName
	}
	if strings.TrimSpace(w.AgentID) == "" {
		return ErrInvalidAgentID
	}
	if !w.Provider.Valid() {
		return ErrInvalidProvider
	}
	if w.WaitTimeoutSeconds < 1 || w.WaitTimeoutSeconds > WaitTimeoutCeilingSeconds || w.MaxRunTimeoutSeconds < 1 || w.MaxRunTimeoutSeconds > RunTimeoutCeilingSeconds {
		return ErrInvalidTimeout
	}
	return nil
}

func newCredential() (credentialRecord, string, error) {
	minted, err := credential.MintOpaqueWithPrefix(TokenPrefix)
	if err != nil {
		return credentialRecord{}, "", fmt.Errorf("webhook: mint capability: %w", err)
	}
	return credentialRecord{Webhook: Webhook{TokenPublicID: minted.PublicID, TokenLast4: minted.Last4}, TokenHash: minted.TokenHash}, minted.Plaintext, nil
}

func matchesTokenHash(secret, hash string) bool {
	return subtle.ConstantTimeCompare([]byte(credential.HashSecret(secret)), []byte(hash)) == 1
}

func hideNotFound(err error) error {
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	return err
}
