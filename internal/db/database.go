package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// migrateLockKey is an arbitrary but stable 64-bit key for the advisory lock
// that serializes migrate() across processes (it spells "stella" in ASCII).
// Never change it: two stellad instances must hash to the same lock to mutually
// exclude.
const migrateLockKey int64 = 0x73_74_65_6C_6C_61

// OpenDB opens the PostgreSQL database at dsn, sizes the connection pool, ensures
// the pg_trgm extension and the schema are present, and returns a pool safe for
// concurrent use. dsn is a libpq/pgx connection string (e.g.
// "postgres://user:pass@host:5432/db?sslmode=disable").
//
// The server must be PostgreSQL 18 or newer: the schema baseline defaults ids
// with the uuidv7() built-in, so migrate() fails with "function uuidv7() does
// not exist" against an older server.
func OpenDB(dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: parse dsn: %w", err)
	}
	// Pin every session to UTC. The codebase stores and compares UTC throughout
	// (time.Now().UTC(), "... AT TIME ZONE 'UTC'"), but PostgreSQL interprets a
	// zoneless timestamp literal in the session's time zone — which otherwise
	// inherits the host's local zone. Without this, the same row round-trips
	// differently on a UTC CI box and a +08 dev box. timezone=UTC rides in the
	// startup packet, so it holds from the first statement on every connection.
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	// Emit one span per query when a parent span is active (see queryTracer).
	cfg.ConnConfig.Tracer = queryTracer{}
	// Treat the native uuid type as text on the wire so sqlc's `string` id/FK
	// fields scan and encode without juggling [16]byte: the app mints and
	// compares uuidv7 strings end to end.
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		tm := conn.TypeMap()
		// TEXT-ONLY is load-bearing, not a style choice. A plain TextCodec also
		// advertises binary support, and pgx's ArrayCodec prefers binary for any
		// element type that supports it — so a uuid[] parameter (ANY($1), bulk id
		// lookups) would ship in binary framing while each element is encoded as
		// the 36-char text form, which PostgreSQL rejects with "improper binary
		// format in array element" (SQLSTATE 22P03). Forcing text-only makes the
		// array fall back to text format, keeping framing and elements consistent.
		uuidType := &pgtype.Type{
			Name:  "uuid",
			OID:   pgtype.UUIDOID,
			Codec: &pgtype.TextFormatOnlyCodec{Codec: pgtype.TextCodec{}},
		}
		tm.RegisterType(uuidType)
		// The default _uuid array codec captured the binary uuid type at init;
		// re-register it against the text uuid element so arrays round-trip as
		// []string in text format too.
		tm.RegisterType(&pgtype.Type{
			Name:  "_uuid",
			OID:   pgtype.UUIDArrayOID,
			Codec: &pgtype.ArrayCodec{ElementType: uuidType},
		})
		return nil
	}
	// PostgreSQL serializes nothing client-side — concurrency is handled
	// server-side by MVCC — so the pool is sized for throughput. The cap is
	// deliberately conservative: many stellad instances can share one server, so
	// per-instance open connections must stay well under the server's
	// max_connections (default 100). A finite lifetime recycles connections so a
	// long-lived process doesn't pin stale backends. Production with high
	// concurrency should front PostgreSQL with pgbouncer or raise these.
	cfg.MaxConns = 20
	cfg.MaxConnLifetime = 30 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return pool, nil
}

// spanCtxKey carries the active query span from TraceQueryStart to
// TraceQueryEnd through the query context.
type spanCtxKey struct{}

// queryTracer is a pgx.QueryTracer that emits one client span per query, but
// only when the caller already holds a valid parent span. Most DB work runs on
// background contexts (startup, scheduler, cache refresh) with no parent —
// emitting there would create one standalone root span per query, drowning real
// request traces in noise. Gating on a valid parent keeps DB spans where they're
// useful: nested under an HTTP request or agent tool span. This mirrors the
// behaviour the otelsql SpanFilter gave the prior database/sql handle.
type queryTracer struct{}

func (queryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	ctx, span := otel.Tracer("github.com/CherryHQ/stella/internal/db").Start(
		ctx, spanName(data.SQL),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "postgresql"),
			attribute.String("db.statement", data.SQL),
		),
	)
	return context.WithValue(ctx, spanCtxKey{}, span)
}

func (queryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span, ok := ctx.Value(spanCtxKey{}).(trace.Span)
	if !ok {
		return
	}
	// pgx.ErrNoRows is an ordinary "not found", not a query failure.
	if data.Err != nil && !errors.Is(data.Err, pgx.ErrNoRows) {
		span.RecordError(data.Err)
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
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
// get the verb; anything unparseable falls back to "query" so a span is never
// left nameless.
func spanName(query string) string {
	name := ""
	if m := reSQLCName.FindStringSubmatch(query); m != nil {
		name = m[1]
	}

	detail := sqlVerbTarget(stripLeadingComments(query))
	if detail == "" {
		detail = "query"
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

// migrate applies pending SQL migration files from the embedded migrations
// directory. Each migration runs in its own transaction (PostgreSQL DDL is
// transactional, so a failed migration rolls back cleanly) and is tracked in a
// schema_migrations table.
//
// All work runs on one pinned connection holding a session-level advisory lock,
// so concurrent stellad processes pointed at the same database serialize: the
// loser blocks on the lock, then sees the winner's applied versions and no-ops.
func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", migrateLockKey)
	}()

	// The baseline's trigram indexes (gin_trgm_ops) depend on pg_trgm, which
	// Atlas (OSS) can't manage declaratively, so create it before applying any
	// migration. Requires a role with CREATE privilege on the database.
	if _, err := conn.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS pg_trgm"); err != nil {
		return fmt.Errorf("ensure pg_trgm extension: %w", err)
	}

	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`
	if _, err := conn.Exec(ctx, createTable); err != nil {
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
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin tx for %s: %w", f, err)
		}
		if _, err := tx.Exec(ctx, string(data)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("exec %s: %w", f, err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO schema_migrations (version) VALUES ($1)", version,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", f, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", f, err)
		}
	}

	// River owns its own schema; bring it up under the same advisory lock so the
	// river_* tables exist wherever the app opens the database.
	if err := migrateRiver(ctx, pool); err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}

	return nil
}

// appliedVersions returns a set of migration versions already recorded
// in schema_migrations.
func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

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
