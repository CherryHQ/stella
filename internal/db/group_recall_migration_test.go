package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	groupRecallBeforeMigration = sequentialAnchor + 21
	groupRecallMigration       = sequentialAnchor + 22
	groupBlobRemovalMigration  = sequentialAnchor + 23
)

func TestGroupRecallMigrationsAreReversibleAndLeaveCurrentSchemaReady(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := groupLCMMigrationProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	assertGroupRecallCurrentSchema(t, ctx, db)

	if _, err := provider.DownTo(ctx, groupRecallBeforeMigration); err != nil {
		t.Fatalf("restore pre-group-recall schema: %v", err)
	}
	assertSchemaObjectExists(t, ctx, db, "ctx_group_memory", true)
	assertSchemaObjectExists(t, ctx, db, "idx_ctx_group_message_bm25", false)

	if _, err := provider.UpTo(ctx, groupRecallMigration); err != nil {
		t.Fatalf("apply group-recall index migration: %v", err)
	}
	assertSchemaObjectExists(t, ctx, db, "ctx_group_memory", true)
	assertGroupRecallIndexReady(t, ctx, db)

	if _, err := provider.UpTo(ctx, groupBlobRemovalMigration); err != nil {
		t.Fatalf("reapply group-recall migrations: %v", err)
	}
	assertGroupRecallCurrentSchema(t, ctx, db)
}

func assertGroupRecallCurrentSchema(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	assertSchemaObjectExists(t, ctx, db, "ctx_group_memory", false)
	assertGroupRecallIndexReady(t, ctx, db)
}

func assertGroupRecallIndexReady(t *testing.T, ctx context.Context, db *pgxpool.Pool) {
	t.Helper()
	var valid, ready bool
	var accessMethod string
	if err := db.QueryRow(ctx, `
		SELECT i.indisvalid, i.indisready, am.amname
		FROM pg_index i
		JOIN pg_class idx ON idx.oid = i.indexrelid
		JOIN pg_am am ON am.oid = idx.relam
		WHERE idx.relname = 'idx_ctx_group_message_bm25'
	`).Scan(&valid, &ready, &accessMethod); err != nil {
		t.Fatalf("read group-message BM25 index state: %v", err)
	}
	if !valid || !ready || accessMethod != "bm25" {
		t.Fatalf("group-message recall index valid=%t ready=%t access_method=%q", valid, ready, accessMethod)
	}
}

func assertSchemaObjectExists(t *testing.T, ctx context.Context, db *pgxpool.Pool, name string, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatalf("check schema object %s: %v", name, err)
	}
	if exists != want {
		t.Fatalf("schema object %s exists=%t, want %t", name, exists, want)
	}
}
