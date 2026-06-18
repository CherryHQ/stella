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
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM ctx_message").Scan(&preWritten); err != nil {
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
		"ctx_message":     11863,
		"recally_article": 53,
		"agent_task":      31,
		"auth_user":       4,
	}
	for table, n := range want {
		if report.Tables[table] != n {
			t.Errorf("report[%s] = %d rows, want %d", table, report.Tables[table], n)
		}
		var got int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(table)).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if got != n {
			t.Errorf("PostgreSQL %s has %d rows, want %d", table, got, n)
		}
	}

	// boolean coercion: SQLite stored 0/1, so every row must read back as a real
	// boolean (true or false), never NULL from a failed cast.
	var typedBool int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM recally_article WHERE starred IS TRUE OR starred IS FALSE").Scan(&typedBool); err != nil {
		t.Fatalf("bool query: %v", err)
	}
	if typedBool != 53 {
		t.Errorf("recally_article boolean-typed rows = %d, want 53", typedBool)
	}

	// timestamptz coercion: both 'YYYY-MM-DD HH:MM:SS' and RFC3339 SQLite strings
	// must parse and compare as timestamps.
	var dated int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM ctx_message WHERE created_at > '2000-01-01'::timestamptz").Scan(&dated); err != nil {
		t.Fatalf("timestamptz query: %v", err)
	}
	if dated != 11863 {
		t.Errorf("ctx_message rows with a valid created_at = %d, want 11863", dated)
	}

	// generated tsvector is computed on insert, so FTS is live right after the
	// migration without any extra backfill step.
	var withTokens int
	if err := db.QueryRowContext(ctx,
		"SELECT count(*) FROM ctx_message WHERE content_tsv <> ''::tsvector").Scan(&withTokens); err != nil {
		t.Fatalf("tsvector query: %v", err)
	}
	if withTokens == 0 {
		t.Error("generated content_tsv is empty for every row; FTS would not work post-migration")
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
