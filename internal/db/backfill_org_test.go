package db_test

import (
	"context"
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

func TestBackfillOrgScopedSettingsNoOpWithNotNullOrgID(t *testing.T) {
	// Since org_id is now NOT NULL, BackfillOrgScopedSettings (which updates
	// WHERE org_id IS NULL) should always be a no-op. Verify it runs without
	// error even when a backfill org exists.
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

	// Run backfill — should be a no-op since org_id is NOT NULL.
	if err := appdb.BackfillOrgScopedSettings(ctx, db); err != nil {
		t.Fatalf("BackfillOrgScopedSettings: %v", err)
	}
}
