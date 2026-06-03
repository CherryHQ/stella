package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// CredentialService manages local password credentials stored in auth_credential.
// These credentials are used by Stella's local password login flow.
type CredentialService struct {
	store CredentialStore
}

// NewCredentialService creates a CredentialService backed by the given store.
func NewCredentialService(store CredentialStore) *CredentialService {
	return &CredentialService{store: store}
}

// SetPassword creates or replaces the local password for a user.
func (s *CredentialService) SetPassword(ctx context.Context, userID, plainPassword string) error {
	hash, err := HashPassword(plainPassword)
	if err != nil {
		return fmt.Errorf("credential: hash password: %w", err)
	}

	_, lookupErr := s.store.GetCredentialByUserID(ctx, userID)
	if lookupErr == nil {
		return s.store.UpdateCredentialHash(ctx, userID, hash)
	}
	if !errors.Is(lookupErr, ErrNotFound) {
		return fmt.Errorf("credential: lookup: %w", lookupErr)
	}

	_, err = s.store.CreateCredential(ctx, Credential{
		ID:           uuid.NewString(),
		UserID:       userID,
		PasswordHash: hash,
	})
	return err
}

// VerifyPassword checks plainPassword against the stored hash for userID.
// Returns ErrNotFound if no credential exists, ErrInvalidCredentials on mismatch.
func (s *CredentialService) VerifyPassword(ctx context.Context, userID, plainPassword string) error {
	cred, err := s.store.GetCredentialByUserID(ctx, userID)
	if err != nil {
		return err
	}
	if err := CheckPassword(cred.PasswordHash, plainPassword); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}

// HasCredential reports whether a credential row exists for userID.
func (s *CredentialService) HasCredential(ctx context.Context, userID string) (bool, error) {
	_, err := s.store.GetCredentialByUserID(ctx, userID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Sentinel errors used across auth domain.
var (
	ErrNotFound           = errors.New("not found")
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrAlreadyConsumed    = errors.New("already consumed")
	ErrExpired            = errors.New("expired")
)
