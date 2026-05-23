package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// BackfillOIDCTables copies legacy auth data into the new OIDC tables on first
// startup. It is idempotent: if auth_user already has rows it returns immediately.
//
// Migration logic:
//   - auth_users → auth_user  (id preserved, username used as email and name)
//   - auth_identities → channel_identity  (id preserved)
//   - one default organization + membership per user (role mirrors auth_users.role)
func BackfillOIDCTables(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auth_user`).Scan(&count); err != nil {
		return 0, fmt.Errorf("oidc backfill: check auth_user: %w", err)
	}
	if count > 0 {
		return 0, nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, username, role, is_active, default_agent_id, notify_identity_id,
		       age_public_key, age_private_key, created_at, updated_at
		FROM auth_users`)
	if err != nil {
		return 0, fmt.Errorf("oidc backfill: list auth_users: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type legacyUser struct {
		id, username, role    string
		isActive              int
		defaultAgentID        *string
		notifyIdentityID      *string
		agePubKey, agePrivKey string
		createdAt, updatedAt  string
	}

	var users []legacyUser
	for rows.Next() {
		var u legacyUser
		if err := rows.Scan(&u.id, &u.username, &u.role, &u.isActive,
			&u.defaultAgentID, &u.notifyIdentityID,
			&u.agePubKey, &u.agePrivKey, &u.createdAt, &u.updatedAt); err != nil {
			return 0, fmt.Errorf("oidc backfill: scan auth_users row: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("oidc backfill: iterate auth_users: %w", err)
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("oidc backfill: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Backfill channel_identity first so we can resolve notify_identity_id FKs.
	ciRows, err := tx.QueryContext(ctx, `SELECT id, user_id, platform, external_id, name, linked_at FROM auth_identities`)
	if err != nil {
		return 0, fmt.Errorf("oidc backfill: list auth_identities: %w", err)
	}
	defer func() { _ = ciRows.Close() }()

	for ciRows.Next() {
		var id, userID, platform, externalID, name, linkedAt string
		if err := ciRows.Scan(&id, &userID, &platform, &externalID, &name, &linkedAt); err != nil {
			return 0, fmt.Errorf("oidc backfill: scan auth_identities row: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO channel_identity (id, user_id, platform, external_id, name, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, userID, platform, externalID, name, linkedAt, linkedAt)
		if err != nil {
			return 0, fmt.Errorf("oidc backfill: insert channel_identity %s: %w", id, err)
		}
	}
	if err := ciRows.Err(); err != nil {
		return 0, fmt.Errorf("oidc backfill: iterate auth_identities: %w", err)
	}

	for _, u := range users {
		// Copy to auth_user (notify_identity_id now points to channel_identity which we just inserted).
		_, err = tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO auth_user
			  (id, email, name, avatar_url, default_agent_id, notify_identity_id,
			   age_public_key, age_private_key, created_at, updated_at)
			VALUES (?, ?, ?, '', ?, ?, ?, ?, ?, ?)`,
			u.id, u.username, u.username,
			u.defaultAgentID, u.notifyIdentityID,
			u.agePubKey, u.agePrivKey, u.createdAt, u.updatedAt)
		if err != nil {
			return 0, fmt.Errorf("oidc backfill: insert auth_user %s: %w", u.id, err)
		}

		// Create a default organization named after the user.
		orgID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO auth_organization (id, name, external_id, source, created_at, updated_at)
			VALUES (?, ?, ?, 'backfill', ?, ?)`,
			orgID, u.username+" (default)", u.id, now, now)
		if err != nil {
			return 0, fmt.Errorf("oidc backfill: insert org for user %s: %w", u.id, err)
		}

		role := "member"
		if u.role == "admin" {
			role = "admin"
		}
		memberID := uuid.NewString()
		_, err = tx.ExecContext(ctx, `
			INSERT INTO auth_membership (id, user_id, organization_id, role, is_active, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			memberID, u.id, orgID, role, u.isActive, now, now)
		if err != nil {
			return 0, fmt.Errorf("oidc backfill: insert membership for user %s: %w", u.id, err)
		}

		slog.Info("oidc backfill: migrated user", "user_id", u.id, "email", u.username, "org_id", orgID)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("oidc backfill: commit: %w", err)
	}

	return len(users), nil
}
