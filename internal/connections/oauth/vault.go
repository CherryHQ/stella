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
