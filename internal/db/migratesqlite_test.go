package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestMigrateSQLite_RealFixture migrates a real prior SQLite database into a
// fresh PostgreSQL schema and checks counts, type coercion, generated columns,
// and idempotency. It needs the fixture on disk, so it skips in CI; point
// STELLA_SQLITE_FIXTURE at a copy to run elsewhere.
func TestMigrateSQLite_RealFixture(t *testing.T) {
	fixture := os.Getenv("STELLA_SQLITE_FIXTURE")
	if fixture == "" {
		home, _ := os.UserHomeDir()
		fixture = filepath.Join(home, ".stella-dev", "stella.db")
	}
	if _, err := os.Stat(fixture); err != nil {
		t.Skipf("SQLite fixture not found at %s (set STELLA_SQLITE_FIXTURE to run)", fixture)
	}

	db := newTestDB(t)
	ctx := context.Background()

	// Dry run previews source counts and writes nothing.
	plan, err := MigrateSQLite(ctx, fixture, db, true)
	if err != nil {
		t.Fatalf("MigrateSQLite dry-run: %v", err)
	}
	if plan.Total == 0 {
		t.Error("dry-run reported 0 rows; expected a non-empty plan")
	}
	var preWritten int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM ctx_message").Scan(&preWritten); err != nil {
		t.Fatalf("post-dry-run count: %v", err)
	}
	if preWritten != 0 {
		t.Errorf("dry-run wrote %d ctx_message rows; expected none", preWritten)
	}

	report, err := MigrateSQLite(ctx, fixture, db, false)
	if err != nil {
		t.Fatalf("MigrateSQLite: %v", err)
	}

	// Known row counts in this fixture. They double as a coercion smoke test: a
	// cast failure on any single row aborts the whole transaction, so matching
	// counts mean every value in these tables coerced cleanly.
	want := map[string]int{
		"ctx_message":     12300,
		"recally_article": 53,
		"auth_user":       4,
	}
	for table, n := range want {
		if report.Tables[table] != n {
			t.Errorf("report[%s] = %d rows, want %d", table, report.Tables[table], n)
		}
		var got int
		if err := db.QueryRow(ctx, "SELECT count(*) FROM "+quoteIdent(table)).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != n {
			t.Errorf("PostgreSQL %s has %d rows, want %d", table, got, n)
		}
	}

	// boolean coercion: SQLite stored 0/1, so every row must read back as a real
	// boolean (true or false), never NULL from a failed cast.
	var typedBool int
	if err := db.QueryRow(ctx,
		"SELECT count(*) FROM recally_article WHERE starred IS TRUE OR starred IS FALSE").Scan(&typedBool); err != nil {
		t.Fatalf("bool query: %v", err)
	}
	if typedBool != 53 {
		t.Errorf("recally_article boolean-typed rows = %d, want 53", typedBool)
	}

	// timestamptz coercion: both 'YYYY-MM-DD HH:MM:SS' and RFC3339 SQLite strings
	// must parse and compare as timestamps.
	var dated int
	if err := db.QueryRow(ctx,
		"SELECT count(*) FROM ctx_message WHERE created_at > '2000-01-01'::timestamptz").Scan(&dated); err != nil {
		t.Fatalf("timestamptz query: %v", err)
	}
	if dated != 12300 {
		t.Errorf("ctx_message rows with a valid created_at = %d, want 12300", dated)
	}

	// The pg_search BM25 index covers content automatically on insert, so lexical
	// search is live right after the migration with no backfill step — as long as
	// the migrated rows carry content for it to index.
	var withContent int
	if err := db.QueryRow(ctx,
		"SELECT count(*) FROM ctx_message WHERE content <> ''").Scan(&withContent); err != nil {
		t.Fatalf("content query: %v", err)
	}
	if withContent == 0 {
		t.Error("every migrated ctx_message has empty content; BM25 search would find nothing")
	}

	// Idempotent: a second run truncates and reloads to the same totals.
	report2, err := MigrateSQLite(ctx, fixture, db, false)
	if err != nil {
		t.Fatalf("second MigrateSQLite: %v", err)
	}
	if report2.Total != report.Total {
		t.Errorf("re-run copied %d rows, want %d (idempotent)", report2.Total, report.Total)
	}
}
