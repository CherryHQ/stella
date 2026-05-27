package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestOpenDBConfiguresSQLiteContentionPolicy(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "contention.db")
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if got := db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	var journalMode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if busyTimeout != int(sqliteBusyTimeout/time.Millisecond) {
		t.Fatalf("busy_timeout = %dms, want %dms", busyTimeout, sqliteBusyTimeout/time.Millisecond)
	}

	var foreignKeys int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		t.Fatalf("PRAGMA foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
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
