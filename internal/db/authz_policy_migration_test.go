package db

import (
	"context"
	"io/fs"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const (
	authzPolicyBeforeRemoval = 20260714024406
	authzPolicyRemoval       = 20260714041417
)

func TestRemoveAuthzPolicyMigrationDropsOnlyInactiveRows(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := authzPolicyProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, authzPolicyBeforeRemoval); err != nil {
		t.Fatalf("restore pre-removal schema: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO authz_policy (id, effect, status) VALUES ('inactive', 'allow', 'inactive')`); err != nil {
		t.Fatalf("seed inactive policy: %v", err)
	}
	if _, err := provider.UpTo(ctx, authzPolicyRemoval); err != nil {
		t.Fatalf("remove inactive policy tables: %v", err)
	}
	if tableExists(t, db, "authz_policy") || tableExists(t, db, "authz_policy_revision") {
		t.Fatal("custom policy tables remain after migration")
	}
}

func TestRemoveAuthzPolicyMigrationRejectsActiveRows(t *testing.T) {
	db := newTestDB(t)
	provider, closeProvider := authzPolicyProvider(t, db)
	defer closeProvider()
	ctx := context.Background()

	if _, err := provider.DownTo(ctx, authzPolicyBeforeRemoval); err != nil {
		t.Fatalf("restore pre-removal schema: %v", err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO authz_policy (id, effect, status) VALUES ('active', 'deny', 'active')`); err != nil {
		t.Fatalf("seed active policy: %v", err)
	}
	if _, err := provider.UpTo(ctx, authzPolicyRemoval); err == nil {
		t.Fatal("migration accepted an active custom policy")
	}
	if !tableExists(t, db, "authz_policy") || !tableExists(t, db, "authz_policy_revision") {
		t.Fatal("failed migration did not roll back policy tables")
	}
	var count int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM authz_policy WHERE id = 'active'`).Scan(&count); err != nil {
		t.Fatalf("read active policy after failed migration: %v", err)
	}
	if count != 1 {
		t.Fatalf("active policy count = %d, want 1", count)
	}
}

func authzPolicyProvider(t *testing.T, pool *pgxpool.Pool) (*goose.Provider, func()) {
	t.Helper()
	migrations, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("create migration provider: %v", err)
	}
	return provider, func() { _ = sqlDB.Close() }
}
