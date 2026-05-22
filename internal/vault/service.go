package vault

import (
	"context"
	"fmt"

	"filippo.io/age"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// DB is the minimal database interface the vault Service requires.
type DB interface {
	GetAuthUser(ctx context.Context, id string) (sqlc.AuthUser, error)
	GetVaultEntry(ctx context.Context, arg sqlc.GetVaultEntryParams) (sqlc.VaultEntry, error)
	ListVaultEntriesByUser(ctx context.Context, userID string) ([]sqlc.VaultEntry, error)
	UpsertVaultEntry(ctx context.Context, arg sqlc.UpsertVaultEntryParams) error
	DeleteVaultEntry(ctx context.Context, arg sqlc.DeleteVaultEntryParams) error
}

// Service provides vault operations: storing, retrieving, and decrypting
// per-user secrets using age encryption.
type Service struct {
	db              DB
	masterIdentity  *age.X25519Identity
	masterRecipient *age.X25519Recipient
}

// NewService creates a vault Service. masterIdentityStr is the raw age secret
// key string (typically from the STELLA_VAULT_KEY environment variable).
func NewService(db DB, masterIdentityStr string) (*Service, error) {
	id, recipient, err := ParseMasterIdentity(masterIdentityStr)
	if err != nil {
		return nil, fmt.Errorf("vault: new service: %w", err)
	}
	return &Service{
		db:              db,
		masterIdentity:  id,
		masterRecipient: recipient,
	}, nil
}

// MasterRecipient returns the master public key recipient.
// It is used when generating new user key pairs.
func (s *Service) MasterRecipient() *age.X25519Recipient {
	return s.masterRecipient
}

// EntryMeta holds non-sensitive metadata for a vault entry.
type EntryMeta struct {
	Name      string
	CreatedAt string
	UpdatedAt string
}

// EncryptSystem encrypts plaintext with the master key for system-level storage
// (not tied to any user).
func (s *Service) EncryptSystem(plaintext string) (string, error) {
	return encryptArmored(s.masterRecipient, plaintext)
}

// DecryptSystem decrypts ciphertext that was produced by EncryptSystem.
func (s *Service) DecryptSystem(ciphertext string) (string, error) {
	return decryptArmored(s.masterIdentity, ciphertext)
}

// Set validates name, encrypts plaintext with the user's public key, and
// upserts the vault entry. The user must already have age keys provisioned.
func (s *Service) Set(ctx context.Context, userID string, name string, plaintext string) error {
	return s.set(ctx, userID, name, plaintext, true)
}

// SetReserved stores an internal reserved env var. Callers must not pass user input.
func (s *Service) SetReserved(ctx context.Context, userID string, name string, plaintext string) error {
	return s.set(ctx, userID, name, plaintext, false)
}

func (s *Service) set(ctx context.Context, userID string, name string, plaintext string, validate bool) error {
	if validate {
		if err := ValidateName(name); err != nil {
			return err
		}
	}

	user, err := s.db.GetAuthUser(ctx, userID)
	if err != nil {
		return fmt.Errorf("vault: set %q: get user: %w", name, err)
	}
	if user.AgePublicKey == "" {
		return fmt.Errorf("vault: set %q: user %s has no age public key provisioned", name, userID)
	}

	ciphertext, err := Encrypt(user.AgePublicKey, plaintext)
	if err != nil {
		return fmt.Errorf("vault: set %q: encrypt: %w", name, err)
	}

	if err := s.db.UpsertVaultEntry(ctx, sqlc.UpsertVaultEntryParams{
		ID:         uuid.NewString(),
		UserID:     userID,
		Name:       name,
		Ciphertext: ciphertext,
	}); err != nil {
		return fmt.Errorf("vault: set %q: upsert: %w", name, err)
	}
	return nil
}

// Delete removes a vault entry by name for the given user.
func (s *Service) Delete(ctx context.Context, userID string, name string) error {
	if err := s.db.DeleteVaultEntry(ctx, sqlc.DeleteVaultEntryParams{
		UserID: userID,
		Name:   name,
	}); err != nil {
		return fmt.Errorf("vault: delete %q: %w", name, err)
	}
	return nil
}

// Get decrypts and returns the plaintext value of a single vault entry by name.
func (s *Service) Get(ctx context.Context, userID string, name string) (string, error) {
	user, err := s.db.GetAuthUser(ctx, userID)
	if err != nil {
		return "", fmt.Errorf("vault: get %q: get user: %w", name, err)
	}
	if user.AgePrivateKey == "" {
		return "", fmt.Errorf("vault: get %q: user %s has no age private key provisioned", name, userID)
	}

	entry, err := s.db.GetVaultEntry(ctx, sqlc.GetVaultEntryParams{
		UserID: userID,
		Name:   name,
	})
	if err != nil {
		return "", fmt.Errorf("vault: get %q: %w", name, err)
	}

	plaintext, err := Decrypt(s.masterIdentity, user.AgePrivateKey, entry.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("vault: get %q: decrypt: %w", name, err)
	}
	return plaintext, nil
}

// List returns metadata for all vault entries owned by userID. Ciphertext is
// never included in the result.
func (s *Service) List(ctx context.Context, userID string) ([]EntryMeta, error) {
	entries, err := s.db.ListVaultEntriesByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("vault: list: %w", err)
	}
	meta := make([]EntryMeta, len(entries))
	for i, e := range entries {
		meta[i] = EntryMeta{
			Name:      e.Name,
			CreatedAt: e.CreatedAt,
			UpdatedAt: e.UpdatedAt,
		}
	}
	return meta, nil
}

// LoadEnv decrypts all vault entries for userID and returns them as a
// name→plaintext map. Intended for injecting secrets into sandbox environments.
func (s *Service) LoadEnv(ctx context.Context, userID string) (map[string]string, error) {
	user, err := s.db.GetAuthUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("vault: load env: get user: %w", err)
	}
	if user.AgePrivateKey == "" {
		return nil, fmt.Errorf("vault: load env: user %s has no age private key provisioned", userID)
	}

	entries, err := s.db.ListVaultEntriesByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("vault: load env: list entries: %w", err)
	}

	env := make(map[string]string, len(entries))
	for _, e := range entries {
		plaintext, err := Decrypt(s.masterIdentity, user.AgePrivateKey, e.Ciphertext)
		if err != nil {
			return nil, fmt.Errorf("vault: load env: decrypt %q: %w", e.Name, err)
		}
		env[e.Name] = plaintext
	}
	return env, nil
}
