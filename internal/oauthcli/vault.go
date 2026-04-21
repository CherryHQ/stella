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

// SaveGHBundle serializes bundle to JSON and stores it under VaultKeyGitHub.
func SaveGHBundle(ctx context.Context, vs VaultStore, userID int64, bundle GHOAuthBundle) error {
	data, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("oauthcli: marshal gh bundle: %w", err)
	}
	if err := vs.Set(ctx, userID, VaultKeyGitHub, string(data)); err != nil {
		return fmt.Errorf("oauthcli: save gh bundle: %w", err)
	}
	return nil
}

// LoadGHBundle retrieves and deserializes the GitHub token bundle for userID.
// Returns nil, nil if no entry exists yet.
func LoadGHBundle(ctx context.Context, vs VaultStore, userID int64) (*GHOAuthBundle, error) {
	env, err := vs.LoadEnv(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("oauthcli: load gh bundle: %w", err)
	}
	raw, ok := env[VaultKeyGitHub]
	if !ok {
		return nil, nil
	}
	var bundle GHOAuthBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("oauthcli: unmarshal gh bundle: %w", err)
	}
	return &bundle, nil
}

// SaveLarkBundle serializes bundle to JSON and stores it under VaultKeyLark.
func SaveLarkBundle(ctx context.Context, vs VaultStore, userID int64, bundle LarkOAuthBundle) error {
	data, err := json.Marshal(bundle)
	if err != nil {
		return fmt.Errorf("oauthcli: marshal lark bundle: %w", err)
	}
	if err := vs.Set(ctx, userID, VaultKeyLark, string(data)); err != nil {
		return fmt.Errorf("oauthcli: save lark bundle: %w", err)
	}
	return nil
}

// LoadLarkBundle retrieves and deserializes the Lark token bundle for userID.
// Returns nil, nil if no entry exists yet.
func LoadLarkBundle(ctx context.Context, vs VaultStore, userID int64) (*LarkOAuthBundle, error) {
	env, err := vs.LoadEnv(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("oauthcli: load lark bundle: %w", err)
	}
	raw, ok := env[VaultKeyLark]
	if !ok {
		return nil, nil
	}
	var bundle LarkOAuthBundle
	if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
		return nil, fmt.Errorf("oauthcli: unmarshal lark bundle: %w", err)
	}
	return &bundle, nil
}

// DeleteBundle removes the vault entry identified by key for userID.
func DeleteBundle(ctx context.Context, vs VaultStore, userID int64, key string) error {
	if err := vs.Delete(ctx, userID, key); err != nil {
		return fmt.Errorf("oauthcli: delete bundle %q: %w", key, err)
	}
	return nil
}
