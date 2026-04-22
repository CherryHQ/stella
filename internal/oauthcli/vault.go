package oauthcli

import (
	"context"
	"encoding/json"
	"fmt"
)

// VaultStore is the narrow interface this package needs from the vault service.
type VaultStore interface {
	Set(ctx context.Context, userID int64, name string, plaintext string) error
	Delete(ctx context.Context, userID int64, name string) error
	LoadEnv(ctx context.Context, userID int64) (map[string]string, error)
}

func saveBundle[T any](ctx context.Context, vs VaultStore, userID int64, key string, bundle T) error {
	data, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("oauthcli: marshal bundle %q: %w", key, err)
	}
	if err := vs.Set(ctx, userID, key, string(data)); err != nil {
		return fmt.Errorf("oauthcli: save bundle %q: %w", key, err)
	}
	return nil
}

func loadBundle[T any](ctx context.Context, vs VaultStore, userID int64, key string) (*T, error) {
	env, err := vs.LoadEnv(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("oauthcli: load bundle %q: %w", key, err)
	}
	raw, ok := env[key]
	if !ok {
		return nil, nil
	}
	var bundle T
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("oauthcli: unmarshal bundle %q: %w", key, err)
	}
	return &bundle, nil
}

// SaveGHBundle serializes bundle to JSON and stores it under VaultKeyGitHub.
func SaveGHBundle(ctx context.Context, vs VaultStore, userID int64, bundle GHOAuthBundle) error {
	return saveBundle(ctx, vs, userID, VaultKeyGitHub, bundle)
}

// LoadGHBundle retrieves and deserializes the GitHub token bundle for userID.
// Returns nil, nil if no entry exists yet.
func LoadGHBundle(ctx context.Context, vs VaultStore, userID int64) (*GHOAuthBundle, error) {
	return loadBundle[GHOAuthBundle](ctx, vs, userID, VaultKeyGitHub)
}

// SaveLarkBundle serializes bundle to JSON and stores it under VaultKeyLark.
func SaveLarkBundle(ctx context.Context, vs VaultStore, userID int64, bundle LarkOAuthBundle) error {
	return saveBundle(ctx, vs, userID, VaultKeyLark, bundle)
}

// LoadLarkBundle retrieves and deserializes the Lark token bundle for userID.
// Returns nil, nil if no entry exists yet.
func LoadLarkBundle(ctx context.Context, vs VaultStore, userID int64) (*LarkOAuthBundle, error) {
	return loadBundle[LarkOAuthBundle](ctx, vs, userID, VaultKeyLark)
}

// DeleteBundle removes the vault entry identified by key for userID.
func DeleteBundle(ctx context.Context, vs VaultStore, userID int64, key string) error {
	if err := vs.Delete(ctx, userID, key); err != nil {
		return fmt.Errorf("oauthcli: delete bundle %q: %w", key, err)
	}
	return nil
}
