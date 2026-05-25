package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// BackfillOIDCTables is retained for gateway.go compatibility. It is a no-op
// now that the legacy auth_users/auth_identities/auth_sessions tables have been
// dropped and all data lives in the OIDC tables.
func BackfillOIDCTables(_ context.Context, _ *sql.DB) (int, error) { return 0, nil }

// BackfillCredentials is retained for gateway.go compatibility. It is a no-op
// now that the legacy auth_users table has been dropped.
func BackfillCredentials(_ context.Context, _ *sql.DB) (int, error) { return 0, nil }

// BackfillOrgScopedSettings assigns existing settings resources (agents, providers,
// channels, policies) that have no org_id to the oldest "backfill" organization.
// This is the local-install "legacy org" created when the first user was migrated.
// Idempotent: resources already assigned to an org are unchanged.
//
// In OIDC-only installs with no backfill org the function is a no-op.
func BackfillOrgScopedSettings(ctx context.Context, db *sql.DB) error {
	// Find the oldest backfill org (created by BackfillOIDCTables).
	var legacyOrgID string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM auth_organization WHERE source='backfill' ORDER BY created_at ASC LIMIT 1`,
	).Scan(&legacyOrgID)
	if err == sql.ErrNoRows {
		return nil // no legacy org — fresh or OIDC-only install, nothing to do
	}
	if err != nil {
		return fmt.Errorf("org backfill: find legacy org: %w", err)
	}

	tables := []string{"settings_agents", "settings_providers", "settings_channels", "auth_policies"}
	for _, tbl := range tables {
		res, err := db.ExecContext(ctx,
			"UPDATE "+tbl+" SET org_id=? WHERE org_id IS NULL",
			legacyOrgID,
		)
		if err != nil {
			return fmt.Errorf("org backfill: update %s: %w", tbl, err)
		}
		n, _ := res.RowsAffected()
		if n > 0 {
			slog.Info("org backfill: assigned resources to legacy org", "table", tbl, "count", n, "org_id", legacyOrgID)
		}
	}
	return nil
}
