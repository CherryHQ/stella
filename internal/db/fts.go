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
	}{
		{"ctx_message_fts", ctxMessageFTSSchema},
		{"ctx_summary_fts", ctxSummaryFTSSchema},
	} {
		// Check the insert trigger alongside the table: an Atlas table-rebuild
		// migration drops the content table's triggers and shifts rowids, which
		// silently stales the index. A missing trigger with the index present is
		// exactly that signature, so it must also force a rebuild.
		var existing int
		if err := db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM sqlite_master WHERE name IN (?, ?)",
			t.ftsTable, t.ftsTable+"_ai",
		).Scan(&existing); err != nil {
			return fmt.Errorf("check %s: %w", t.ftsTable, err)
		}
		if _, err := db.ExecContext(ctx, t.schema); err != nil {
			if strings.Contains(err.Error(), "fts5") {
				return fmt.Errorf("create %s: sqlite build lacks FTS5 support, required for memory search: %w", t.ftsTable, err)
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
