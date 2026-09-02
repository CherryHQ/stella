package db

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	pgxvec "github.com/pgvector/pgvector-go/pgx"
	"github.com/pressly/goose/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/CherryHQ/stella/internal/platform/observability"
)

// migrateLockKey is an arbitrary but stable 64-bit key for the advisory lock
// that serializes migrate() across processes (it spells "stella" in ASCII).
// Never change it: two stellad instances must hash to the same lock to mutually
// exclude.
const migrateLockKey int64 = 0x73_74_65_6C_6C_61

// OpenDB opens the PostgreSQL database at dsn, sizes the connection pool, ensures
// required PostgreSQL extensions and the schema are present, and returns a pool safe for
// concurrent use. dsn is a libpq/pgx connection string (e.g.
// "postgres://user:pass@host:5432/db?sslmode=disable").
//
// The server must be PostgreSQL 18 or newer: the schema baseline defaults ids
// with the uuidv7() built-in, so migrate() fails with "function uuidv7() does
// not exist" against an older server.
func OpenDB(dsn string, opts ...Option) (*pgxpool.Pool, error) {
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
	// Tag connections so they are distinguishable in pg_stat_activity and server
	// logs across the many stellad instances that may share one server.
	cfg.ConnConfig.RuntimeParams["application_name"] = "stellad"
	// Bound a transaction that opens then stalls (a wedged agent run, a leaked tx):
	// without this it pins its backend, locks, and MVCC snapshot indefinitely on the
	// shared server. statement_timeout is deliberately left off — agent runs are
	// legitimately long; only the idle-in-transaction case is dangerous.
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "60s"
	// Fail fast when the host is unreachable or accepts TCP but never answers,
	// instead of blocking startup and lazy acquisitions forever — pgx/libpq sets no
	// default connect timeout. Applies to every dial, eager or lazy.
	cfg.ConnConfig.ConnectTimeout = 10 * time.Second
	// Emit one span per query when a parent span is active (see queryTracer).
	cfg.ConnConfig.Tracer = queryTracer{}
	// Treat the native uuid type as text on the wire so sqlc's `string` id/FK
	// fields scan and encode without juggling [16]byte: the app mints and
	// compares uuidv7 strings end to end.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
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
		// Register the pgvector codecs so the embedding sidecar tables' `vector`
		// columns (pgvector.Vector in the sqlc models) encode and scan: pgx dispatches
		// by registered OID, and the vector OID is assigned dynamically by CREATE
		// EXTENSION. On a fresh database the type does not exist yet when this hook
		// fires for migrate()'s first connection, so probe and skip until it does;
		// every connection opened after the extension exists (all of them on every
		// later boot) gets the codec, and the lone pre-extension migrate connection
		// recycles within MaxConnLifetime. Without this, the first vector query once
		// the embedding backfill lands would fail to (de)serialize pgvector.Vector.
		var hasVector bool
		if err := conn.QueryRow(ctx, "SELECT to_regtype('vector') IS NOT NULL").Scan(&hasVector); err != nil {
			return fmt.Errorf("probe vector type: %w", err)
		}
		if hasVector {
			if err := pgxvec.RegisterTypes(ctx, conn); err != nil {
				return fmt.Errorf("register pgvector types: %w", err)
			}
		}
		return nil
	}
	// PostgreSQL serializes nothing client-side — concurrency is handled
	// server-side by MVCC — so the pool is sized for throughput. The cap is
	// deliberately conservative: many stellad instances can share one server, so
	// per-instance open connections must stay well under the server's
	// max_connections (default 100). A finite lifetime recycles connections so a
	// long-lived process doesn't pin stale backends. Production with high
	// concurrency should front PostgreSQL with pgbouncer or raise these.
	//
	// Pooler note: connections use pgx's default prepared-statement exec mode and
	// migrate() takes a session-scoped advisory lock — both require a SESSION-pooling
	// pgbouncer. For transaction pooling, use pgbouncer >= 1.21 with
	// max_prepared_statements set and run migrations against a direct connection.
	cfg.MaxConns = 20
	// A small warm floor so a burst right after idle reaping doesn't all pay full
	// connect + TLS + per-connection codec-registration latency.
	cfg.MinConns = 2
	cfg.MaxConnLifetime = 30 * time.Minute
	// Desynchronize recycling so connections opened together at startup don't all
	// tear down and reconnect within the same brief window.
	cfg.MaxConnLifetimeJitter = 5 * time.Minute
	cfg.MaxConnIdleTime = 5 * time.Minute

	// Apply caller overrides last so they win over the defaults above (tests size
	// the pool down — many parallel test databases each opening a 20-conn pool can
	// approach the embedded server's max_connections).
	for _, opt := range opts {
		opt(cfg)
	}

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

// Option overrides pool configuration after OpenDB has applied its defaults.
type Option func(*pgxpool.Config)

// WithMaxConns caps the pool size. Production uses the built-in default; tests
// pass a small value so many parallel test databases stay well under the
// embedded server's max_connections.
func WithMaxConns(n int32) Option {
	return func(c *pgxpool.Config) {
		c.MaxConns = n
		if c.MinConns > n {
			c.MinConns = n
		}
	}
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
	if os.Getenv("OTEL_STELLA_RECORD_DB_QUERIES") != "true" || !trace.SpanContextFromContext(ctx).IsValid() {
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
		observability.RecordSpanError(span, data.Err, "db query failed")
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

// migrate ensures the required extensions exist, then applies pending goose
// migrations from the embedded migrations directory and brings up River's
// schema. goose tracks applied versions in goose_db_version and runs each
// migration in its own transaction (PostgreSQL DDL is transactional, so a
// failed migration rolls back cleanly).
//
// Extensions and the advisory lock run on one pinned connection: the lock
// serializes concurrent stellad processes pointed at the same database, so the
// loser blocks, then sees the winner's applied versions and no-ops.
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

	// The search stack's vector (HNSW) and bm25 indexes depend on extensions that
	// migrations cannot create declaratively, so install required extensions
	// before applying any migration. Requires a role with CREATE privilege on the
	// database.
	if err := ensureExtensions(ctx, conn); err != nil {
		return err
	}

	if err := runMigrations(ctx, pool); err != nil {
		return err
	}

	// River owns its own schema; bring it up under the same advisory lock so the
	// river_* tables exist wherever the app opens the database.
	if err := migrateRiver(ctx, pool); err != nil {
		return fmt.Errorf("migrate river: %w", err)
	}

	return nil
}

// runMigrations applies all pending goose migrations from the embedded
// migrations directory. goose runs on a database/sql handle backed by the same
// pgx pool (closing it does not close the pool). The caller already holds the
// cross-process advisory lock; a goose Provider carries no global state, so it
// is also safe under the in-process concurrency of parallel test databases.
func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	sub, err := fs.Sub(MigrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("open migrations fs: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
