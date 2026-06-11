package db

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenDBCreatesFTSIndexes(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "fts.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"ctx_message_fts", "ctx_summary_fts", "recally_article_fts"} {
		if !tableExists(t, db, table) {
			t.Errorf("%s should exist after OpenDB", table)
		}
	}
}

func TestEnsureFTSBackfillsPreexistingRows(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "fts-backfill.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Simulate a database created before FTS support existed: no index, no
	// triggers, but rows already present in the content tables.
	for _, stmt := range []string{
		"DROP TABLE ctx_message_fts",
		"DROP TABLE ctx_summary_fts",
		"DROP TABLE recally_article_fts",
		"DROP TRIGGER IF EXISTS ctx_message_fts_ai",
		"DROP TRIGGER IF EXISTS ctx_message_fts_ad",
		"DROP TRIGGER IF EXISTS ctx_message_fts_au",
		"DROP TRIGGER IF EXISTS ctx_summary_fts_ai",
		"DROP TRIGGER IF EXISTS ctx_summary_fts_ad",
		"DROP TRIGGER IF EXISTS ctx_summary_fts_au",
		"DROP TRIGGER IF EXISTS recally_article_fts_ai",
		"DROP TRIGGER IF EXISTS recally_article_fts_ad",
		"DROP TRIGGER IF EXISTS recally_article_fts_au",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ctx_conversation (id, session_id) VALUES ('conv-fts', 'sess-fts')`); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ctx_message (id, conversation_id, seq, role, content, token_count)
		VALUES ('msg-fts', 'conv-fts', 1, 'user', 'legacy aardvark message', 5)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ctx_summary (id, conversation_id, kind, depth, content, token_count)
		VALUES ('sum-fts', 'conv-fts', 'leaf', 0, 'legacy aardvark summary', 5)`); err != nil {
		t.Fatalf("insert summary: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO auth_user (id, email) VALUES ('user-fts', 'fts@example.com')`); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO recally_article (id, user_id, url, canonical_url, title)
		VALUES ('art-fts', 'user-fts', 'https://example.com/a', 'https://example.com/a', 'legacy aardvark article')`); err != nil {
		t.Fatalf("insert article: %v", err)
	}

	if err := ensureFTS(db); err != nil {
		t.Fatalf("ensureFTS: %v", err)
	}

	for _, table := range []string{"ctx_message_fts", "ctx_summary_fts", "recally_article_fts"} {
		var count int
		if err := db.QueryRow(
			"SELECT COUNT(*) FROM " + table + " WHERE " + table + " MATCH 'aardvark'",
		).Scan(&count); err != nil {
			t.Fatalf("match on %s: %v", table, err)
		}
		if count != 1 {
			t.Errorf("%s: expected 1 backfilled hit, got %d", table, count)
		}
	}
}

func TestEnsureFTSRebuildsAfterTriggerLoss(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "fts-triggerloss.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Simulate an Atlas table-rebuild migration: the FTS index survives but the
	// content table's triggers are gone, so rows written afterwards are missing
	// from the index until ensureFTS detects the loss and rebuilds.
	for _, stmt := range []string{
		"DROP TRIGGER ctx_message_fts_ai",
		"DROP TRIGGER ctx_message_fts_ad",
		"DROP TRIGGER ctx_message_fts_au",
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ctx_conversation (id, session_id) VALUES ('conv-tl', 'sess-tl')`); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO ctx_message (id, conversation_id, seq, role, content, token_count)
		VALUES ('msg-tl', 'conv-tl', 1, 'user', 'orphaned pangolin message', 5)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	if err := ensureFTS(db); err != nil {
		t.Fatalf("ensureFTS: %v", err)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM ctx_message_fts WHERE ctx_message_fts MATCH 'pangolin'",
	).Scan(&count); err != nil {
		t.Fatalf("match: %v", err)
	}
	if count != 1 {
		t.Errorf("expected rebuilt index to find the orphaned row, got %d hits", count)
	}
}

func TestEnsureFTSRebuildsOnTokenizerDrift(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "fts-tokenizer-drift.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Simulate a database from before the trigram switch: same table and
	// triggers, but built with the old unicode61 tokenizer. CREATE IF NOT
	// EXISTS alone would leave it stale, so ensureFTS must detect the
	// tokenizer mismatch, drop, recreate, and rebuild.
	for _, stmt := range []string{
		"DROP TABLE ctx_message_fts",
		`CREATE VIRTUAL TABLE ctx_message_fts USING fts5(
			content,
			content='ctx_message',
			content_rowid='rowid',
			tokenize='unicode61 remove_diacritics 1'
		)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := db.Exec(`INSERT INTO ctx_conversation (id, session_id) VALUES ('conv-tok', 'sess-tok')`); err != nil {
		t.Fatalf("insert conversation: %v", err)
	}
	// The surviving insert trigger indexes this row with unicode61, where a
	// CJK substring query cannot match it.
	if _, err := db.Exec(`INSERT INTO ctx_message (id, conversation_id, seq, role, content, token_count)
		VALUES ('msg-tok', 'conv-tok', 1, 'user', '今天讨论了部署方案', 5)`); err != nil {
		t.Fatalf("insert message: %v", err)
	}

	if err := ensureFTS(db); err != nil {
		t.Fatalf("ensureFTS: %v", err)
	}

	var ddl string
	if err := db.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='ctx_message_fts'`,
	).Scan(&ddl); err != nil {
		t.Fatalf("read DDL: %v", err)
	}
	if !strings.Contains(ddl, "tokenize='trigram'") {
		t.Fatalf("expected trigram table after ensureFTS, got DDL: %s", ddl)
	}

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM ctx_message_fts WHERE ctx_message_fts MATCH '部署方案'",
	).Scan(&count); err != nil {
		t.Fatalf("match: %v", err)
	}
	if count != 1 {
		t.Errorf("expected CJK substring hit after tokenizer rebuild, got %d", count)
	}
}
