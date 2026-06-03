package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

	db, err := sql.Open("sqlite", dataSourceName(dbPath))
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	configurePool(db)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return db, nil
}

// dataSourceName builds the modernc.org/sqlite DSN. Pragmas are carried in the
// DSN so every pooled connection is configured identically at open time; this
// is what makes a multi-connection pool safe (a one-shot PRAGMA via db.Exec
// would only configure whichever connection happened to run it). WAL lets
// readers run concurrently with a single writer, busy_timeout serializes
// writers without surfacing SQLITE_BUSY, and foreign keys stay enforced.
func dataSourceName(dbPath string) string {
	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout.Milliseconds()))
	q.Add("_pragma", "foreign_keys(ON)")
	return dbPath + "?" + q.Encode()
}

func configurePool(db *sql.DB) {
	if db == nil {
		return
	}
	// WAL mode allows many concurrent readers alongside one writer, so the pool
	// is sized for read parallelism. Writes still serialize inside SQLite and
	// wait out contention via busy_timeout rather than failing.
	maxConns := max(runtime.NumCPU()*4, 8)
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
}

// OpenSerialConn opens a second handle to an already-migrated SQLite database
// capped at a single connection. Write-heavy subsystems (the memory provider)
// run on this handle so their writes queue in Go as a fast FIFO instead of
// fighting over SQLite's single write lock across many pooled connections —
// which otherwise burns the busy_timeout and starves the shared read pool.
//
// The caller must have run OpenDB on the same path first; this handle does not
// migrate. The DSN carries the same pragmas (WAL, busy_timeout, foreign keys),
// and WAL lets this writer run concurrently with readers on the main pool.
func OpenSerialConn(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dataSourceName(dbPath))
	if err != nil {
		return nil, fmt.Errorf("db: open serial conn: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	db.SetConnMaxIdleTime(0)
	return db, nil
}

// migrate applies pending SQL migration files from the embedded migrations
// directory. Each migration is executed in its own transaction and tracked
// in a schema_migrations table.
//
// All work runs on a single pinned connection: toggling PRAGMA foreign_keys
// around each transaction is connection-scoped, so the disable, the
// transaction, and the re-enable must land on the same underlying connection.
// With a multi-connection pool, issuing them against *sql.DB directly could
// scatter them across connections and run table-rebuild migrations with
// foreign keys still on.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	// Create tracking table.
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`
	if _, err := conn.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	// Collect applied versions.
	applied, err := appliedVersions(ctx, conn)
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
		// PRAGMA foreign_keys cannot be changed inside a transaction in SQLite
		// (the statement is silently ignored). Disable it on the connection
		// before the transaction so Atlas-style table-rebuild migrations work,
		// then re-enable it after commit.
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = off"); err != nil {
			return fmt.Errorf("disable fk for %s: %w", f, err)
		}
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			_ = tx.Rollback()
			_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = on")
			return fmt.Errorf("exec %s: %w", f, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version) VALUES (?)", version,
		); err != nil {
			_ = tx.Rollback()
			_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = on")
			return fmt.Errorf("record %s: %w", f, err)
		}
		if err := tx.Commit(); err != nil {
			_, _ = conn.ExecContext(ctx, "PRAGMA foreign_keys = on")
			return fmt.Errorf("commit %s: %w", f, err)
		}
		if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys = on"); err != nil {
			return fmt.Errorf("re-enable fk after %s: %w", f, err)
		}
	}

	return nil
}

// appliedVersions returns a set of migration versions already recorded
// in schema_migrations.
func appliedVersions(ctx context.Context, conn *sql.Conn) (map[string]bool, error) {
	rows, err := conn.QueryContext(ctx, "SELECT version FROM schema_migrations")
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
