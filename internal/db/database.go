package db

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	sqliteBusyTimeout = 5 * time.Second
)

// OpenDB opens a SQLite database at the given path, configures it
// (WAL mode, foreign keys, busy timeout) and runs migrations. The parent directory
// is created if it doesn't exist.
func OpenDB(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("db: create dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	configurePool(db)
	if err := ConfigureDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return db, nil
}

// ConfigureDB enables the SQLite connection policy Anna relies on.
// Connection-level pragmas are safe here because OpenDB constrains each handle
// to a single long-lived underlying connection.
func ConfigureDB(db *sql.DB) error {
	configurePool(db)
	pragmas := []struct {
		query string
		name  string
	}{
		{query: "PRAGMA journal_mode=WAL", name: "enable WAL"},
		{query: fmt.Sprintf("PRAGMA busy_timeout=%d", sqliteBusyTimeout.Milliseconds()), name: "set busy timeout"},
		{query: "PRAGMA foreign_keys=ON", name: "enable foreign keys"},
	}
	for _, pragma := range pragmas {
		if _, err := db.Exec(pragma.query); err != nil {
			return fmt.Errorf("db: %s: %w", pragma.name, err)
		}
	}
	return nil
}

func configurePool(db *sql.DB) {
	if db == nil {
		return
	}
	// SQLite only allows one writer at a time. Keeping one underlying
	// connection per handle makes connection-scoped PRAGMAs deterministic and
	// avoids self-contention inside a single *sql.DB pool.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
}

// migrate applies pending SQL migration files from the embedded migrations
// directory. Each migration is executed in its own transaction and tracked
// in a schema_migrations table.
func migrate(db *sql.DB) error {
	// Create tracking table.
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := db.Exec(createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Collect applied versions.
	applied, err := appliedVersions(db)
	if err != nil {
		return fmt.Errorf("read applied versions: %w", err)
	}

	// Read and sort migration files.
	files, err := migrationFiles()
	if err != nil {
		return fmt.Errorf("read migration files: %w", err)
	}

	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			continue
		}
		data, err := MigrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			return fmt.Errorf("read %s: %w", f, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		if _, err := tx.Exec(string(data)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec %s: %w", f, err)
		}
		if _, err := tx.Exec(
			"INSERT INTO schema_migrations (version) VALUES (?)", version,
		); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", f, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", f, err)
		}
	}

	return nil
}

// appliedVersions returns a set of migration versions already recorded
// in schema_migrations.
func appliedVersions(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// migrationFiles returns .sql filenames from the embedded migrations
// directory in sorted (chronological) order.
func migrationFiles() ([]string, error) {
	entries, err := fs.ReadDir(MigrationsFS, "migrations")
	if err != nil {
		return nil, err
	}

	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}
