package oauth

import "context"

// SaveOAuthBundle serializes bundle to JSON and stores it under the given
// vault key for userID.
func SaveOAuthBundle(ctx context.Context, vs VaultStore, userID string, key string, bundle OAuthBundle) error {
	return saveBundle(ctx, vs, userID, key, bundle)
}

// LoadOAuthBundle retrieves and deserializes the OAuth bundle for userID under
// the given vault key. Returns nil, nil if no entry exists yet.
func LoadOAuthBundle(ctx context.Context, vs VaultStore, userID string, key string) (*OAuthBundle, error) {
	return loadBundle[OAuthBundle](ctx, vs, userID, key)
}
