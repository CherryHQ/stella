package channel

import (
	"context"
	"fmt"

	"github.com/vaayne/anna/internal/config"
)

// ResolveUser upserts a user by external ID + platform, returning the user record.
func ResolveUser(ctx context.Context, store config.Store, externalID, platform, name string) (config.User, error) {
	user, err := store.UpsertUser(ctx, externalID, platform, name)
	if err != nil {
		return config.User{}, fmt.Errorf("resolve user: %w", err)
	}
	return user, nil
}
