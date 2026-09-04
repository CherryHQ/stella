package oauth

import (
	"context"
	"encoding/json"
	"fmt"
)

// VaultStore is the narrow interface this package needs from the vault service.
type VaultStore interface {
	Set(ctx context.Context, userID string, name string, plaintext string) error
	Delete(ctx context.Context, userID string, name string) error
	Lookup(ctx context.Context, userID string, name string) (string, bool, error)
}

// ScopedVaultStore is the vault surface needed by OAuth credentials whose
// owner may be a user, agent, or system registration.
type ScopedVaultStore interface {
	SetScoped(ctx context.Context, scope, userID, agentID, name, plaintext string) error
	SetSystemScoped(ctx context.Context, scope, agentID, name, plaintext string) error
	GetScoped(ctx context.Context, scope, userID, agentID, name string) (string, error)
	DeleteScoped(ctx context.Context, scope, userID, agentID, name string) error
	DeleteSystemScoped(ctx context.Context, scope, agentID, name string) error
}

// BundleStore persists versioned OAuth bundles without exposing vault
// encryption or scope-specific write rules to TokenManager.
type BundleStore interface {
	Load(ctx context.Context, ref BundleRef) (*OAuthBundle, error)
	Save(ctx context.Context, ref BundleRef, bundle OAuthBundle) error
	Delete(ctx context.Context, ref BundleRef) error
}

type userBundleStore struct{ vault VaultStore }

// NewUserBundleStore adapts the legacy user-only vault surface.
func NewUserBundleStore(vs VaultStore) BundleStore { return userBundleStore{vault: vs} }

func (s userBundleStore) Load(ctx context.Context, ref BundleRef) (*OAuthBundle, error) {
	return LoadOAuthBundle(ctx, s.vault, ref.Owner.UserID, ref.Name)
}

func (s userBundleStore) Save(ctx context.Context, ref BundleRef, bundle OAuthBundle) error {
	return SaveOAuthBundle(ctx, s.vault, ref.Owner.UserID, ref.Name, bundle)
}

func (s userBundleStore) Delete(ctx context.Context, ref BundleRef) error {
	return DeleteBundle(ctx, s.vault, ref.Owner.UserID, ref.Name)
}

type scopedBundleStore struct{ vault ScopedVaultStore }

// NewScopedBundleStore adapts the full vault ownership tuple.
func NewScopedBundleStore(vs ScopedVaultStore) BundleStore { return scopedBundleStore{vault: vs} }

func (s scopedBundleStore) Load(ctx context.Context, ref BundleRef) (*OAuthBundle, error) {
	raw, err := s.vault.GetScoped(ctx, ref.Owner.Scope, ref.Owner.UserID, ref.Owner.AgentID, ref.Name)
	if err != nil {
		return nil, fmt.Errorf("oauth: load bundle %q: %w", ref.Name, err)
	}
	if raw == "" {
		return nil, nil
	}
	var bundle OAuthBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("oauth: unmarshal bundle %q: %w", ref.Name, err)
	}
	return &bundle, nil
}

func (s scopedBundleStore) Save(ctx context.Context, ref BundleRef, bundle OAuthBundle) error {
	raw, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("oauth: marshal bundle %q: %w", ref.Name, err)
	}
	if ref.Owner.Scope == "system" || ref.Owner.Scope == "system_agent" {
		err = s.vault.SetSystemScoped(ctx, ref.Owner.Scope, ref.Owner.AgentID, ref.Name, string(raw))
	} else {
		err = s.vault.SetScoped(ctx, ref.Owner.Scope, ref.Owner.UserID, ref.Owner.AgentID, ref.Name, string(raw))
	}
	if err != nil {
		return fmt.Errorf("oauth: save bundle %q: %w", ref.Name, err)
	}
	return nil
}

func (s scopedBundleStore) Delete(ctx context.Context, ref BundleRef) error {
	var err error
	if ref.Owner.Scope == "system" || ref.Owner.Scope == "system_agent" {
		err = s.vault.DeleteSystemScoped(ctx, ref.Owner.Scope, ref.Owner.AgentID, ref.Name)
	} else {
		err = s.vault.DeleteScoped(ctx, ref.Owner.Scope, ref.Owner.UserID, ref.Owner.AgentID, ref.Name)
	}
	if err != nil {
		return fmt.Errorf("oauth: delete bundle %q: %w", ref.Name, err)
	}
	return nil
}

func saveBundle[T any](ctx context.Context, vs VaultStore, userID string, key string, bundle T) error {
	data, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("oauth: marshal bundle %q: %w", key, err)
	}
	if err := vs.Set(ctx, userID, key, string(data)); err != nil {
		return fmt.Errorf("oauth: save bundle %q: %w", key, err)
	}
	return nil
}

func loadBundle[T any](ctx context.Context, vs VaultStore, userID string, key string) (*T, error) {
	raw, ok, err := vs.Lookup(ctx, userID, key)
	if err != nil {
		return nil, fmt.Errorf("oauth: load bundle %q: %w", key, err)
	}
	if !ok {
		return nil, nil
	}
	var bundle T
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("oauth: unmarshal bundle %q: %w", key, err)
	}
	return &bundle, nil
}

// DeleteBundle removes the vault entry identified by key for userID.
func DeleteBundle(ctx context.Context, vs VaultStore, userID string, key string) error {
	if err := vs.Delete(ctx, userID, key); err != nil {
		return fmt.Errorf("oauth: delete bundle %q: %w", key, err)
	}
	return nil
}
