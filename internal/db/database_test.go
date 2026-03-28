package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestOpenDBFreshInstallDoesNotCreateFeishuTokensTable(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "fresh.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if tableExists(t, db, "feishu_tokens") {
		t.Fatal("feishu_tokens table should not exist after fresh install migrations")
	}
}

func TestOpenDBDropsLegacyFeishuTokensTableOnUpgrade(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	if err := ConfigureDB(db); err != nil {
		t.Fatalf("ConfigureDB: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}

	files, err := migrationFiles()
	if err != nil {
		t.Fatalf("migrationFiles: %v", err)
	}

	const dropVersion = "20260328084600_drop_feishu_tokens"
	for _, file := range files {
		version := strings.TrimSuffix(file, ".sql")
		if version == dropVersion {
			continue
		}
		if _, err := db.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			t.Fatalf("seed schema_migrations %s: %v", version, err)
		}
	}

	_, err = db.Exec(`CREATE TABLE feishu_tokens (
		open_id TEXT PRIMARY KEY,
		access_token TEXT NOT NULL,
		refresh_token TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		refresh_expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (datetime('now')),
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	if err != nil {
		t.Fatalf("create feishu_tokens: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close seeded db: %v", err)
	}

	upgraded, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB upgrade: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })

	if tableExists(t, upgraded, "feishu_tokens") {
		t.Fatal("feishu_tokens table should be dropped during upgrade migrations")
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		name,
	).Scan(&count); err != nil {
		t.Fatalf("query sqlite_master for %s: %v", name, err)
	}
	return count > 0
}
