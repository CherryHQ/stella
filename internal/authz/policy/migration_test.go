package policy

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

const (
	// version of this subphase's migration and the one immediately before it.
	migrationVersion = 20260711181515
	priorVersion     = 20260711134243
)

func newGooseProvider(t *testing.T, pool *pgxpool.Pool) (*goose.Provider, func()) {
	t.Helper()
	sub, err := fs.Sub(appdb.MigrationsFS, "migrations")
	if err != nil {
		t.Fatalf("open migrations fs: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("goose provider: %v", err)
	}
	return provider, func() { _ = sqlDB.Close() }
}

func tableExists(t *testing.T, pool *pgxpool.Pool, name string) bool {
	t.Helper()
	var exists bool
	err := pool.QueryRow(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, name).Scan(&exists)
	if err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}
	return exists
}

// The full Down/Up cycle around this migration: Down drops both additive tables
// (and never touches auth_policy); Up re-creates them and backfills existing
// legacy rows as quarantined with an operator-readable reason.
func TestMigrationDownUpBackfillsAndQuarantinesLegacyRows(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	provider, closeDB := newGooseProvider(t, pool)
	defer closeDB()

	// Roll back to just before this migration.
	if _, err := provider.DownTo(ctx, priorVersion); err != nil {
		t.Fatalf("down to prior: %v", err)
	}
	if tableExists(t, pool, "authz_policy") || tableExists(t, pool, "authz_policy_revision") {
		t.Fatal("Down must drop both authz tables")
	}
	// auth_policy is untouched by this migration and must survive the Down.
	if !tableExists(t, pool, "auth_policy") {
		t.Fatal("Down must not drop legacy auth_policy")
	}

	// Seed a legacy custom policy row that predates the typed catalog.
	if _, err := pool.Exec(ctx,
		`INSERT INTO auth_policy (id, name, effect, priority) VALUES ('legacy-1', 'Legacy Custom', 'allow', 5)`); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// Re-apply this migration.
	if _, err := provider.UpTo(ctx, migrationVersion); err != nil {
		t.Fatalf("up to migration: %v", err)
	}

	// Revision counter is a single row seeded at 0.
	var count, rev int64
	if err := pool.QueryRow(ctx, `SELECT count(*), coalesce(max(revision),-1) FROM authz_policy_revision`).Scan(&count, &rev); err != nil {
		t.Fatalf("read revision row: %v", err)
	}
	if count != 1 || rev != 0 {
		t.Fatalf("revision table has count=%d rev=%d, want 1 row at 0", count, rev)
	}

	// The legacy row was copied in as quarantined, never active, with a reason.
	var status, reason string
	if err := pool.QueryRow(ctx,
		`SELECT status, quarantine_reason FROM authz_policy WHERE id = 'legacy-1'`).Scan(&status, &reason); err != nil {
		t.Fatalf("read backfilled row: %v", err)
	}
	if status != statusQuarantined {
		t.Fatalf("legacy row status = %q, want quarantined", status)
	}
	if reason == "" {
		t.Fatal("quarantined legacy row must carry an operator-readable reason")
	}
	var activeCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM authz_policy WHERE status = 'active'`).Scan(&activeCount); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if activeCount != 0 {
		t.Fatalf("backfill produced %d active rows, want 0 (all legacy rows quarantined)", activeCount)
	}
}

// The revision counter must be a single UPDATE-locked row, not a sequence:
// sequence allocation order is not commit order. Assert the migration source
// uses no sequence/serial/nextval, and that no sequence exists in the schema for
// the authz tables.
func TestNoSequenceForRevisionCounter(t *testing.T) {
	raw, err := fs.ReadFile(appdb.MigrationsFS, "migrations/20260711181515_add_authz_policy_revision.sql")
	if err != nil {
		t.Fatalf("read migration source: %v", err)
	}
	// Scan only executable DDL, not the `-- ...` comments (which legitimately
	// explain WHY no sequence/nextval is used).
	var ddl strings.Builder
	for line := range strings.SplitSeq(string(raw), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		ddl.WriteString(strings.ToLower(line))
		ddl.WriteString("\n")
	}
	src := ddl.String()
	for _, banned := range []string{"nextval", "create sequence", "serial", "bigserial"} {
		if strings.Contains(src, banned) {
			t.Fatalf("migration must not use %q — revision order must be commit order via a locked counter row", banned)
		}
	}

	ctx := context.Background()
	pool := dbtest.New(t)
	var seqCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.sequences WHERE sequence_name LIKE 'authz_policy%'`).Scan(&seqCount); err != nil {
		t.Fatalf("query sequences: %v", err)
	}
	if seqCount != 0 {
		t.Fatalf("found %d sequences for authz_policy*, want 0", seqCount)
	}
}

func TestQuarantineListingSurfacesDiagnostics(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	// Insert a quarantined row directly (as the migration backfill would).
	if _, err := pool.Exec(ctx, `INSERT INTO authz_policy (id, effect, status, quarantine_reason)
		VALUES ('q1', 'allow', 'quarantined', 'legacy shape not interpretable')`); err != nil {
		t.Fatalf("seed quarantined row: %v", err)
	}
	svc := NewService(New(pool))
	rows, err := svc.ListQuarantined(ctx)
	if err != nil {
		t.Fatalf("list quarantined: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "q1" || rows[0].Reason == "" {
		t.Fatalf("quarantine listing = %+v, want one diagnosable row", rows)
	}
}
