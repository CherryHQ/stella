package providercred

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
)

// Service is the encryption boundary for Agent Provider credentials. It
// validates and encrypts plaintext Input before it reaches the Store, so
// plaintext never enters SQL arguments. It owns no authorization; a higher layer
// gates who may call these methods.
type Service struct {
	store  Store
	cipher SecretCipher
}

// NewService wires a persistence Store to a SecretCipher.
func NewService(store Store, cipher SecretCipher) *Service {
	return &Service{store: store, cipher: cipher}
}

// List returns secret-free credential metadata for one Agent.
func (s *Service) List(ctx context.Context, agentID string) ([]Metadata, error) {
	if s == nil || s.store == nil {
		return nil, ErrUnavailable
	}
	return s.store.ListAgentProviderCredentials(ctx, agentID)
}

// Set validates, encrypts, and upserts one credential. Rotation is atomic: the
// key is encrypted before the write, and the write itself replaces the row in a
// single statement, so a failure at either step leaves the previous ciphertext
// intact.
func (s *Service) Set(ctx context.Context, agentID string, input Input) error {
	if err := validateInput(input); err != nil {
		return err
	}
	enc, err := s.encrypt(input)
	if err != nil {
		return err
	}
	if s.store == nil {
		return ErrUnavailable
	}
	if err := s.store.UpsertAgentProviderCredential(ctx, agentID, enc); err != nil {
		return fmt.Errorf("provider credential: set %q: %w", input.ProviderID, err)
	}
	return nil
}

// Delete removes one credential, restoring global-key fallback. It is
// idempotent.
func (s *Service) Delete(ctx context.Context, agentID, providerID string) error {
	if providerID == "" {
		return ErrEmptyProviderID
	}
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.store.DeleteAgentProviderCredential(ctx, agentID, providerID); err != nil {
		return fmt.Errorf("provider credential: delete %q: %w", providerID, err)
	}
	return nil
}

// CreateAgentWithCredentials validates and encrypts every input, then persists
// the Agent and all credential rows atomically. If any key fails to encrypt,
// nothing is written. An empty inputs slice creates the Agent with no overrides.
func (s *Service) CreateAgentWithCredentials(ctx context.Context, agent config.Agent, inputs []Input) error {
	if err := validateSet(inputs); err != nil {
		return err
	}
	creds := make([]Encrypted, len(inputs))
	for i, in := range inputs {
		enc, err := s.encrypt(in)
		if err != nil {
			return err
		}
		creds[i] = enc
	}
	if s == nil || s.store == nil {
		return ErrUnavailable
	}
	if err := s.store.CreateAgentWithCredentials(ctx, agent, creds); err != nil {
		return fmt.Errorf("provider credential: create agent %q: %w", agent.ID, err)
	}
	return nil
}

func (s *Service) encrypt(input Input) (Encrypted, error) {
	if s == nil || s.cipher == nil {
		return Encrypted{}, ErrUnavailable
	}
	ciphertext, err := s.cipher.EncryptSystem(input.APIKey)
	if err != nil {
		return Encrypted{}, fmt.Errorf("provider credential: encrypt %q: %w", input.ProviderID, err)
	}
	return Encrypted{ProviderID: input.ProviderID, APIKeyEnc: ciphertext}, nil
}

func validateInput(input Input) error {
	if input.ProviderID == "" {
		return ErrEmptyProviderID
	}
	if input.APIKey == "" {
		return ErrEmptyAPIKey
	}
	return nil
}

func validateSet(inputs []Input) error {
	seen := make(map[string]struct{}, len(inputs))
	for _, in := range inputs {
		if err := validateInput(in); err != nil {
			return err
		}
		if _, dup := seen[in.ProviderID]; dup {
			return fmt.Errorf("%w: %s", ErrDuplicateProvider, in.ProviderID)
		}
		seen[in.ProviderID] = struct{}{}
	}
	return nil
}
