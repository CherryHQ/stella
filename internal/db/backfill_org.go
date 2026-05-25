package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// BackfillOIDCTables is retained for gateway.go compatibility. It is a no-op
// now that the legacy auth_users/auth_identities/auth_sessions tables have been
// dropped and all data lives in the OIDC tables.
func BackfillOIDCTables(_ context.Context, _ *sql.DB) (int, error) { return 0, nil }

// BackfillCredentials is retained for gateway.go compatibility. It is a no-op
// now that the legacy auth_users table has been dropped.
func BackfillCredentials(_ context.Context, _ *sql.DB) (int, error) { return 0, nil }

// EnsureDefaultOrg returns the ID of the default organization, creating one if
// none exists. It first checks for an existing org (any source); if none is
// found it creates a "default" org with source "seed". Idempotent.
func EnsureDefaultOrg(ctx context.Context, db *sql.DB) (string, error) {
	var orgID string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM auth_organization ORDER BY created_at ASC LIMIT 1`,
	).Scan(&orgID)
	if err == nil {
		return orgID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("ensure default org: %w", err)
	}
	orgID = uuid.New().String()
	_, err = db.ExecContext(ctx,
		`INSERT INTO auth_organization (id, name, external_id, source) VALUES (?, ?, ?, ?)`,
		orgID, "Default", "", "seed",
	)
	if err != nil {
		return "", fmt.Errorf("ensure default org: create: %w", err)
	}
	slog.Info("created default organization for seed", "org_id", orgID)
	return orgID, nil
}

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
