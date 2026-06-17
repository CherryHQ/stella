package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/XSAM/otelsql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// migrateLockKey is an arbitrary but stable 64-bit key for the advisory lock
// that serializes migrate() across processes (it spells "stella" in ASCII).
// Never change it: two stellad instances must hash to the same lock to mutually
// exclude.
const migrateLockKey int64 = 0x73_74_65_6C_6C_61

// OpenDB opens the PostgreSQL database at dsn, sizes the connection pool, ensures
// the pg_trgm extension and the schema are present, and returns a handle safe for
// concurrent use. dsn is a libpq/pgx connection string (e.g.
// "postgres://user:pass@host:5432/db?sslmode=disable").
func OpenDB(dsn string) (*sql.DB, error) {
	// otelsql wraps the driver so every query/exec emits a span on the global
	// tracer provider. When tracing is disabled the provider is a no-op, so this
	// adds negligible overhead — same always-on pattern as the HTTP handler.
	// Only statements are recorded (SQL text, no bound args), and the noisy
	// connection-lifecycle spans (connect, prepare, reset, per-row) are omitted
	// to keep traces focused on actual queries.
	db, err := openTracedPG(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	configurePool(db)

	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	if err := ensureFTS(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("db: fts: %w", err)
	}

	return db, nil
}

// reSQLCName extracts the query name from the sqlc annotation that prefixes
// every generated statement, e.g. "-- name: GetUserByID :one".
var reSQLCName = regexp.MustCompile(`(?m)^\s*--\s*name:\s*(\w+)`)

// SQL target extractors: pull the table name following the keyword that names
// it for each statement kind. Identifiers are plain (sqlc emits unquoted table
// names), so a bare word match is enough.
var (
	reFromTarget   = regexp.MustCompile(`(?is)\bfrom\s+(\w+)`)
	reIntoTarget   = regexp.MustCompile(`(?is)\binto\s+(\w+)`)
	reUpdateTarget = regexp.MustCompile(`(?is)\bupdate\s+(\w+)`)
)

// spanName builds a readable span name for a SQL statement. sqlc keeps its
// "-- name: X" annotation as the first line of every generated query, so when
// present it names the span "X (VERB table)" (e.g.
// "GetUserByID (SELECT auth_user)") — the query name says intent, the verb and
// table say what it touches. Ad-hoc statements with no annotation get just
// "VERB table"; statements without a clear table (BEGIN, SELECT pg_advisory_lock)
// get the verb; anything unparseable falls back to the otelsql method so a span
// is never left nameless.
func spanName(_ context.Context, method otelsql.Method, query string) string {
	name := ""
	if m := reSQLCName.FindStringSubmatch(query); m != nil {
		name = m[1]
	}

	detail := sqlVerbTarget(stripLeadingComments(query))
	if detail == "" {
		detail = string(method)
	}
	if name != "" {
		return name + " (" + detail + ")"
	}
	return detail
}

// sqlVerbTarget returns "VERB table" (or just "VERB" when no table is clear)
// for a statement whose leading comments have been stripped. Empty string if
// the statement has no leading keyword to read.
func sqlVerbTarget(stmt string) string {
	fields := strings.Fields(stmt)
	if len(fields) == 0 {
		return ""
	}
	verb := strings.ToUpper(fields[0])
	var re *regexp.Regexp
	switch verb {
	case "SELECT", "DELETE":
		re = reFromTarget
	case "INSERT", "REPLACE":
		re = reIntoTarget
	case "UPDATE":
		re = reUpdateTarget
	default:
		return verb
	}
	if m := re.FindStringSubmatch(stmt); m != nil {
		return verb + " " + m[1]
	}
	return verb
}

// stripLeadingComments drops leading blank and "--" comment lines so the first
// remaining token is the SQL verb. Needed because sqlc prefixes each query with
// its "-- name:" annotation.
func stripLeadingComments(q string) string {
	lines := strings.Split(q, "\n")
	for i, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		return strings.Join(lines[i:], "\n")
	}
	return ""
}

func openTracedPG(dsn string) (*sql.DB, error) {
	return otelsql.Open("pgx", dsn, pgTraceOptions()...)
}

func pgTraceOptions() []otelsql.Option {
	return []otelsql.Option{
		otelsql.WithAttributes(attribute.String("db.system", "postgresql")),
		// Name spans "<verb> <table>" (e.g. "SELECT sessions") instead of the
		// generic "sql.conn.query" so the trace tree shows what each query does
		// at a glance, without expanding the db.statement attribute.
		otelsql.WithSpanNameFormatter(spanName),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			OmitConnectorConnect: true,
			OmitConnResetSession: true,
			OmitConnPrepare:      true,
			OmitRows:             true,
			// Only emit a query span when the caller already has an active span.
			// Most DB work runs on background contexts (startup, scheduler,
			// cache refresh) with no parent — those would become standalone
			// root spans, one per query, drowning real request traces in noise.
			// Gating on a valid parent keeps DB spans where they're useful:
			// nested under an HTTP request or agent tool span.
			SpanFilter: func(ctx context.Context, _ otelsql.Method, _ string, _ []driver.NamedValue) bool {
				return trace.SpanContextFromContext(ctx).IsValid()
			},
		}),
	}
}

func configurePool(db *sql.DB) {
	if db == nil {
		return
	}
	// PostgreSQL serializes nothing client-side — concurrency is handled
	// server-side by MVCC — so the pool is sized for throughput. The cap is
	// deliberately conservative: many stellad instances can share one server, so
	// per-instance open connections must stay well under the server's
	// max_connections (default 100). A finite lifetime recycles connections so a
	// long-lived process doesn't pin stale backends. Production with high
	// concurrency should front PostgreSQL with pgbouncer or raise these.
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)
}

// migrate applies pending SQL migration files from the embedded migrations
// directory. Each migration runs in its own transaction (PostgreSQL DDL is
// transactional, so a failed migration rolls back cleanly) and is tracked in a
// schema_migrations table.
//
// All work runs on one pinned connection holding a session-level advisory lock,
// so concurrent stellad processes pointed at the same database serialize: the
// loser blocks on the lock, then sees the winner's applied versions and no-ops.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", migrateLockKey)
	}()

	// The baseline's trigram indexes (gin_trgm_ops) depend on pg_trgm, which
	// Atlas (OSS) can't manage declaratively, so create it before applying any
	// migration. Requires a role with CREATE privilege on the database.
	if _, err := conn.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		return fmt.Errorf("ensure pg_trgm extension: %w", err)
	}

	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	if _, err := conn.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return fmt.Errorf("read applied versions: %w", err)
	}

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
		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("exec %s: %w", f, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1)", version,
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
