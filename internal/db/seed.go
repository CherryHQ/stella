package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/CherryHQ/stella/internal/auth"
)

// EnsureDefaultOrg returns the ID of the default organization, creating one if
// none exists. It first checks for an existing org (any source); if none is
// found it creates a seed org. Idempotent.
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
		orgID, auth.DefaultOrgName, "", "seed",
	)
	if err != nil {
		return "", fmt.Errorf("ensure default org: create: %w", err)
	}
	slog.Info("created default organization for seed", "org_id", orgID)
	return orgID, nil
}
