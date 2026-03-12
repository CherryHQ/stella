package memory

import (
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	appdb "github.com/vaayne/anna/db"
	_ "modernc.org/sqlite"
)

// OpenDB opens a SQLite database at the given path, applies WAL mode
// and runs migrations. The parent directory is created if it doesn't exist.
func OpenDB(dbPath string) (*sql.DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("memory: create db dir: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}

	// Enable WAL mode for concurrent reads during writes.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("memory: enable WAL: %w", err)
	}

	// Enable foreign keys.
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("memory: enable foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}

	return db, nil
}

// currentSchemaVersion is the latest schema version this binary understands.
const currentSchemaVersion = 2

// migrate applies schema migrations using PRAGMA user_version to track state.
func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}

	switch version {
	case 0:
		// Check if tables exist (could be version 0 with existing schema
		// from before we started tracking versions).
		var tableName string
		err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='conversations'").Scan(&tableName)
		if err == nil {
			// Tables exist but no version set — this is version 1 (original schema).
			return migrateV1ToV2(db)
		}
		// Fresh database — apply full schema.
		return applyFreshSchema(db)

	case 1:
		return migrateV1ToV2(db)

	case currentSchemaVersion:
		return nil // current version, nothing to do
	}

	return fmt.Errorf("unsupported schema version %d", version)
}

// applyFreshSchema creates all tables from embedded SQL files and sets the
// user_version to the current schema version.
func applyFreshSchema(db *sql.DB) error {
	schema, err := readSchemaFiles()
	if err != nil {
		return fmt.Errorf("read schema: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

// migrateV1ToV2 adds columns introduced in schema version 2:
//   - conversations: channel, archived, last_active
//   - messages: event_type
func migrateV1ToV2(db *sql.DB) error {
	// SQLite ALTER TABLE does not support non-constant defaults like
	// datetime('now'), so we use a constant default and backfill.
	alters := []string{
		"ALTER TABLE conversations ADD COLUMN channel TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE conversations ADD COLUMN archived INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE conversations ADD COLUMN last_active TEXT NOT NULL DEFAULT '1970-01-01 00:00:00'",
		"ALTER TABLE messages ADD COLUMN event_type TEXT NOT NULL DEFAULT 'text'",
	}
	for _, stmt := range alters {
		if _, err := db.Exec(stmt); err != nil {
			// Column might already exist — check for "duplicate column" error.
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("alter: %w", err)
			}
		}
	}

	// Backfill last_active from created_at for existing rows.
	if _, err := db.Exec("UPDATE conversations SET last_active = created_at WHERE last_active = '1970-01-01 00:00:00'"); err != nil {
		return fmt.Errorf("backfill last_active: %w", err)
	}
	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", currentSchemaVersion)); err != nil {
		return fmt.Errorf("set user_version: %w", err)
	}
	return nil
}

// isDuplicateColumnError returns true if the error indicates the column
// already exists in the table.
func isDuplicateColumnError(err error) bool {
	return strings.Contains(err.Error(), "duplicate column")
}

// readSchemaFiles reads all table SQL files from the embedded FS and
// concatenates them in sorted order.
func readSchemaFiles() (string, error) {
	var files []string
	err := fs.WalkDir(appdb.SchemaFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".sql") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	sort.Strings(files)

	var b strings.Builder
	for _, f := range files {
		data, err := appdb.SchemaFS.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("read %s: %w", f, err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}

	return b.String(), nil
}
