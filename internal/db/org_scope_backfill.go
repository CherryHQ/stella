package db

import (
	"database/sql"
	"fmt"
)

// backfillLegacyOrgScope moves pre-org-scope singleton rows into every org.
// These tables used to be deployment-global; duplicating preserves behavior
// while all new reads and writes use explicit org_id filters.
func backfillLegacyOrgScope(db *sql.DB) error {
	statements := []string{
		`INSERT OR IGNORE INTO plugin_oauth_provider (
			id, provider_id, client_id, client_secret_enc, redirect_url, org_id, created_at, updated_at
		)
		SELECT
			p.id || ':' || o.id,
			p.provider_id,
			p.client_id,
			p.client_secret_enc,
			p.redirect_url,
			o.id,
			p.created_at,
			p.updated_at
		FROM plugin_oauth_provider p
		CROSS JOIN auth_organization o
		WHERE p.org_id IS NULL`,
		`DELETE FROM plugin_oauth_provider WHERE org_id IS NULL`,
		`INSERT OR IGNORE INTO settings_plugin_state (
			plugin_id, scope_kind, scope_id, state_key, value, org_id, created_at, updated_at
		)
		SELECT
			s.plugin_id,
			s.scope_kind,
			s.scope_id,
			s.state_key,
			s.value,
			o.id,
			s.created_at,
			s.updated_at
		FROM settings_plugin_state s
		CROSS JOIN auth_organization o
		WHERE s.org_id IS NULL`,
		`DELETE FROM settings_plugin_state WHERE org_id IS NULL`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("legacy org scope statement: %w", err)
		}
	}
	return nil
}
