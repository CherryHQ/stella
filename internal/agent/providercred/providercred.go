// Package providercred owns per-Agent LLM Provider API-key overrides. A row here
// replaces only the API key an Agent uses for one canonical global Provider;
// every other Provider attribute (type, base URL, model catalog, enabled state)
// stays global. Keys are encrypted at rest through a SecretCipher and are
// write-only: plaintext exists only on the internal Input type below, ciphertext
// only on Encrypted, and neither ever reaches config.Agent or a JSON boundary.
//
// This package holds the domain types and the persistence/cipher ports. The
// concrete Store is DBStore; the cipher is vault.Service. Authorization and the
// runtime Snapshot overlay live in later layers, not here.
package providercred

import (
	"context"
	"errors"
	"time"

	"github.com/CherryHQ/stella/internal/config"
)

// Transport limits keep credential writes bounded without putting plaintext on a
// transport or persistence type. The OpenAPI contract mirrors these values.
const (
	MaxCredentialsPerCreate = 8
	MaxProviderIDLength     = 255
	MaxAPIKeyLength         = 16384
)

// Validation errors are returned before any secret work happens, so callers can
// map them to a 400 without risking partial writes.
var (
	// ErrEmptyProviderID rejects a credential with no target Provider.
	ErrEmptyProviderID = errors.New("provider credential: provider_id is required")
	// ErrEmptyAPIKey rejects an empty key. DELETE, not an empty write, is how a
	// caller restores global-key fallback.
	ErrEmptyAPIKey = errors.New("provider credential: api_key must not be empty")
	// ErrDuplicateProvider rejects a create/collection that names the same
	// canonical Provider twice.
	ErrDuplicateProvider = errors.New("provider credential: duplicate provider_id")
	// ErrTooManyCredentials rejects a create collection beyond the supported
	// bounded cardinality.
	ErrTooManyCredentials = errors.New("provider credential: too many credentials")
	// ErrProviderIDTooLong rejects an identifier that cannot be accepted by the
	// bounded API contract.
	ErrProviderIDTooLong = errors.New("provider credential: provider_id is too long")
	// ErrAPIKeyTooLong rejects an oversized write-only key before encryption.
	ErrAPIKeyTooLong = errors.New("provider credential: api_key is too long")
	// ErrUnavailable reports missing persistence or encryption dependencies. It
	// is safe for a transport layer to map to 503.
	ErrUnavailable = errors.New("provider credential: unavailable")
)

// Input is a plaintext credential submission. It is internal-only: APIKey is
// live secret material and is explicitly excluded from JSON. The type must never
// be logged or placed on a transport response DTO.
type Input struct {
	ProviderID string
	APIKey     string `json:"-"`
}

// String and GoString make accidental fmt logging redact the plaintext key.
func (i Input) String() string {
	return "provider credential input for " + i.ProviderID + " [REDACTED]"
}
func (i Input) GoString() string { return i.String() }

// Encrypted is the persistence representation of a credential. APIKeyEnc is
// ciphertext produced by a SecretCipher, explicitly excluded from JSON, and is
// the only key form the Store ever sees.
type Encrypted struct {
	ProviderID string
	APIKeyEnc  string `json:"-"`
}

// String and GoString also hide ciphertext: encrypted credentials remain
// sensitive and do not belong in logs.
func (e Encrypted) String() string {
	return "encrypted provider credential for " + e.ProviderID + " [REDACTED]"
}
func (e Encrypted) GoString() string { return e.String() }

// Metadata is the secret-free projection of a stored credential, safe to return
// to any caller allowed to read the Agent. A stored row always carries a
// non-empty key (enforced by a DB CHECK), so HasAPIKey is true for every listed
// row; the field exists for the write-only metadata contract.
type Metadata struct {
	ProviderID string
	HasAPIKey  bool
	UpdatedAt  time.Time
}

// SecretCipher encrypts and decrypts credential material. vault.Service satisfies
// it through EncryptSystem/DecryptSystem, keeping vault out of the persistence
// layer.
type SecretCipher interface {
	EncryptSystem(plaintext string) (string, error)
	DecryptSystem(ciphertext string) (string, error)
}

// Store persists encrypted credentials. Every value crossing this port is
// ciphertext or metadata — plaintext never does.
type Store interface {
	// ListAgentProviderCredentials returns secret-free metadata for one Agent.
	ListAgentProviderCredentials(ctx context.Context, agentID string) ([]Metadata, error)
	// GetAgentProviderCredential returns one credential's ciphertext, or found
	// false when no override exists.
	GetAgentProviderCredential(ctx context.Context, agentID, providerID string) (Encrypted, bool, error)
	// UpsertAgentProviderCredential writes (or atomically rotates) one credential.
	UpsertAgentProviderCredential(ctx context.Context, agentID string, cred Encrypted) (Metadata, error)
	// DeleteAgentProviderCredential removes one credential; it is idempotent.
	DeleteAgentProviderCredential(ctx context.Context, agentID, providerID string) error
	// CreateAgentWithCredentials inserts the Agent and all its credential rows in
	// one transaction: either every row lands or none do.
	CreateAgentWithCredentials(ctx context.Context, agent config.Agent, creds []Encrypted) error
}
