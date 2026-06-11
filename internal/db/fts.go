package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
)

// FTS5 indexes live outside the Atlas migration pipeline because Atlas's
// embedded dev SQLite lacks the fts5 module. The DDL (virtual tables +
// sync triggers) is kept in the sqlc schema dir so generated queries can
// reference the virtual tables, and is applied here at startup instead.
var (
	//go:embed schemas/tables/ctx_message_fts.sql
	ctxMessageFTSSchema string
	//go:embed schemas/tables/ctx_summary_fts.sql
	ctxSummaryFTSSchema string
	// RecallyArticleFTSSchema is exported so recally store tests, which
	// assemble their schema by hand instead of running migrations, can create
	// the index and triggers their search queries depend on.
	//go:embed schemas/tables/recally_article_fts.sql
	RecallyArticleFTSSchema string
)

// ensureFTS creates the FTS5 virtual tables and triggers if missing, and
// backfills the index from existing rows on first creation so pre-existing
// history becomes searchable. Called from OpenDB after migrations succeed;
// OpenSerialConn skips it because its caller must run OpenDB first.
func ensureFTS(db *sql.DB) error {
	ctx := context.Background()
	for _, t := range []struct {
		ftsTable string
		schema   string
		tokenize string
	}{
		{"ctx_message_fts", ctxMessageFTSSchema, "trigram"},
		{"ctx_summary_fts", ctxSummaryFTSSchema, "trigram"},
		{"recally_article_fts", RecallyArticleFTSSchema, "trigram"},
	} {
		// A table created with an older tokenizer (unicode61) would survive the
		// CREATE IF NOT EXISTS below with a stale, incompatible index. Drop it
		// so the embedded DDL recreates it and the rebuild path backfills it.
		var ddl sql.NullString
		_ = db.QueryRowContext(ctx,
			`SELECT sql FROM sqlite_master WHERE type='table' AND name = ?`, t.ftsTable,
		).Scan(&ddl) // ErrNoRows means no table yet; the create below handles it
		if ddl.Valid && !strings.Contains(ddl.String, "tokenize='"+t.tokenize+"'") {
			if _, err := db.ExecContext(ctx, `DROP TABLE `+t.ftsTable); err != nil {
				return fmt.Errorf("drop stale %s: %w", t.ftsTable, err)
			}
		}
		// Check the insert trigger alongside the table: an Atlas table-rebuild
		// migration drops the content table's triggers and shifts rowids, which
		// silently stales the index. A missing trigger with the index present is
		// exactly that signature, so it must also force a rebuild. A tokenizer
		// drop above also lands here (table missing → existing < 2 → rebuild).
		var existing int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE name IN (?, ?)",
			t.ftsTable, t.ftsTable+"_ai",
		).Scan(&existing); err != nil {
			return fmt.Errorf("check %s: %w", t.ftsTable, err)
		}
		if _, err := db.ExecContext(ctx, t.schema); err != nil {
			if strings.Contains(err.Error(), "fts5") {
				return fmt.Errorf("create %s: sqlite build lacks FTS5 support, required for full-text search: %w", t.ftsTable, err)
			}
			return fmt.Errorf("create %s: %w", t.ftsTable, err)
		}
		// External-content tables start empty; rebuild scans the content table
		// so rows inserted before the triggers existed become searchable.
		if existing < 2 {
			rebuild := fmt.Sprintf("INSERT INTO %s(%s) VALUES('rebuild')", t.ftsTable, t.ftsTable)
			if _, err := db.ExecContext(ctx, rebuild); err != nil {
				return fmt.Errorf("backfill %s: %w", t.ftsTable, err)
			}
		}
	}
	return nil
}
