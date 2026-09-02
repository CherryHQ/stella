package providercred

import (
	"context"
	"fmt"

	"github.com/CherryHQ/stella/internal/config"
)

// CredentialReader is the read side the overlay needs: one referenced provider's
// ciphertext, or found=false when the Agent has no override. providercred.Store
// (and thus DBStore) satisfies it.
type CredentialReader interface {
	GetAgentProviderCredential(ctx context.Context, agentID, providerID string) (Encrypted, bool, error)
}

// CredentialLoader decorates a base config.SnapshotLoader, overlaying per-Agent
// Provider API-key overrides onto the assembled Snapshot. It changes only API
// keys — provider type, base URL, model catalog, and enabled state stay exactly
// as the base produced them.
//
// It fails closed: a referenced override whose ciphertext will not decrypt — or
// that has no cipher at all, e.g. a restart without STELLA_VAULT_KEY — returns an
// error rather than silently serving the global key. It only reads overrides for
// canonical providers the Snapshot currently references, so a dormant override
// (for a provider no model tier uses) is never touched and a corrupt dormant row
// cannot break an unrelated Agent. When no referenced override exists, the base
// Snapshot passes through unchanged, so keyless deployments keep working.
type CredentialLoader struct {
	base   config.SnapshotLoader
	store  CredentialReader
	cipher SecretCipher
}

var _ config.SnapshotLoader = (*CredentialLoader)(nil)

// NewCredentialLoader wraps base with the credential overlay. store supplies
// ciphertext; cipher decrypts it. cipher may be nil (no STELLA_VAULT_KEY): the
// loader still queries referenced rows and fails closed if one is found, so a
// dropped key can never downgrade an override to the global key silently.
func NewCredentialLoader(base config.SnapshotLoader, store CredentialReader, cipher SecretCipher) *CredentialLoader {
	return &CredentialLoader{base: base, store: store, cipher: cipher}
}

// Snapshot loads the base Snapshot and overlays any Agent Provider key overrides.
func (l *CredentialLoader) Snapshot(ctx context.Context, agentID string) (*config.Snapshot, error) {
	if l == nil || l.base == nil {
		return nil, ErrUnavailable
	}
	snap, err := l.base.Snapshot(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if l.store == nil {
		return nil, ErrUnavailable
	}
	if snap == nil {
		return snap, nil
	}

	// The canonical provider IDs the Snapshot actually references. Only these are
	// looked up, so a dormant override is never read and a corrupt dormant row
	// cannot fail this load.
	canonical := make(map[string]struct{}, len(snap.Providers))
	for _, creds := range snap.Providers {
		if creds.ProviderID != "" {
			canonical[creds.ProviderID] = struct{}{}
		}
	}

	// Decrypt each referenced override at most once.
	keys := make(map[string]string, len(canonical))
	for providerID := range canonical {
		enc, found, err := l.store.GetAgentProviderCredential(ctx, agentID, providerID)
		if err != nil {
			return nil, fmt.Errorf("provider credential overlay: load %q for agent %q: %w", providerID, agentID, err)
		}
		if !found {
			continue // no override → global key stands
		}
		if l.cipher == nil {
			// A referenced override exists but there is no cipher to decrypt it (e.g.
			// the deployment restarted without STELLA_VAULT_KEY). Fail closed rather
			// than fall through to the global key.
			return nil, fmt.Errorf("provider credential overlay: %q for agent %q: %w", providerID, agentID, ErrUnavailable)
		}
		plaintext, err := l.cipher.DecryptSystem(enc.APIKeyEnc)
		if err != nil {
			// Fail closed: a referenced override that will not decrypt must never
			// fall through to the global key.
			return nil, fmt.Errorf("provider credential overlay: decrypt %q for agent %q: %w", providerID, agentID, err)
		}
		keys[providerID] = plaintext
	}
	if len(keys) == 0 {
		return snap, nil
	}

	// Apply each decrypted key to every map entry — canonical or type alias — that
	// resolves to the same canonical provider.
	for id, creds := range snap.Providers {
		if key, ok := keys[creds.ProviderID]; ok {
			creds.APIKey = key
			snap.Providers[id] = creds
		}
	}
	// Keep the legacy top-level default key coherent with the default provider's
	// entry so consumers reading Snapshot.APIKey observe the override too.
	if def, ok := snap.Providers[snap.Provider]; ok {
		if key, ok := keys[def.ProviderID]; ok {
			snap.APIKey = key
		}
	}
	return snap, nil
}
