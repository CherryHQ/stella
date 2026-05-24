package db_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/google/uuid"

	appdb "github.com/CherryHQ/stella/internal/db"
)

func TestBackfillOrgScopedSettingsNoOp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "org_backfill_test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	// No backfill orgs → should be a no-op.
	if err := appdb.BackfillOrgScopedSettings(context.Background(), db); err != nil {
		t.Fatalf("BackfillOrgScopedSettings: %v", err)
	}
}

func TestBackfillOrgScopedSettingsAssignsToLegacyOrg(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "org_backfill2_test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	now := "2026-01-01 00:00:00"
	orgID := uuid.NewString()

	// Insert a backfill org.
	_, err = db.ExecContext(ctx,
		`INSERT INTO auth_organization (id, name, external_id, source, created_at, updated_at) VALUES (?, ?, ?, 'backfill', ?, ?)`,
		orgID, "Legacy Org", "ext1", now, now,
	)
	if err != nil {
		t.Fatalf("insert org: %v", err)
	}

	// Insert an agent without org_id.
	agentID := uuid.NewString()
	_, err = db.ExecContext(ctx,
		`INSERT INTO settings_agents (id, name, workspace, model, soul) VALUES (?, 'TestAgent', '/tmp', '', '')`,
		agentID,
	)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Run backfill.
	if err := appdb.BackfillOrgScopedSettings(ctx, db); err != nil {
		t.Fatalf("BackfillOrgScopedSettings: %v", err)
	}

	// Verify agent got org_id.
	var gotOrgID sql.NullString
	err = db.QueryRowContext(ctx, `SELECT org_id FROM settings_agents WHERE id=?`, agentID).Scan(&gotOrgID)
	if err != nil {
		t.Fatalf("query agent org_id: %v", err)
	}
	if !gotOrgID.Valid || gotOrgID.String != orgID {
		t.Errorf("agent org_id = %v, want %q", gotOrgID, orgID)
	}

	// Running again should be idempotent (already assigned).
	if err := appdb.BackfillOrgScopedSettings(ctx, db); err != nil {
		t.Fatalf("BackfillOrgScopedSettings (second run): %v", err)
	}
}
