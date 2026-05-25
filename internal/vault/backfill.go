package vault

import (
	"context"
	"fmt"
	"log/slog"

	"filippo.io/age"

	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// BackfillDB is the minimal database interface required by BackfillUserKeys.
type BackfillDB interface {
	ListVaultUsers(ctx context.Context) ([]sqlc.VaultUserRecord, error)
	UpdateVaultUserAgeKeys(ctx context.Context, id, publicKey, privateKey string) error
}

// BackfillUserKeys generates age keypairs for any users that don't have them yet.
// Called at startup when STELLA_VAULT_KEY is configured.
// Returns the number of users updated.
func BackfillUserKeys(ctx context.Context, db BackfillDB, masterRecipient *age.X25519Recipient) (int, error) {
	users, err := db.ListVaultUsers(ctx)
	if err != nil {
		return 0, fmt.Errorf("vault: backfill: list users: %w", err)
	}

	var updated int
	for _, u := range users {
		if u.AgePublicKey != "" {
			continue
		}

		pubKey, encPrivKey, err := GenerateUserKeys(masterRecipient)
		if err != nil {
			return updated, fmt.Errorf("vault: backfill: generate keys for user %s: %w", u.ID, err)
		}

		if err := db.UpdateVaultUserAgeKeys(ctx, u.ID, pubKey, encPrivKey); err != nil {
			return updated, fmt.Errorf("vault: backfill: update keys for user %s: %w", u.ID, err)
		}

		slog.Info("vault: backfill: provisioned age keys", "user_id", u.ID)
		updated++
	}

	return updated, nil
}
