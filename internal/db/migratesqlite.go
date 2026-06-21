package db

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "modernc.org/sqlite"
)

// MigrateReport summarizes a SQLite→PostgreSQL data copy.
type MigrateReport struct {
	Tables    map[string]int // target table → rows copied
	Total     int            // total rows across all tables
	Sanitized int            // values whose invalid UTF-8 was replaced
	Converted int            // uuid values salvaged from a legacy-prefixed string
	Dropped   int            // rows skipped: a uuid column held an unsalvageable value
	Skipped   []string       // target tables with no SQLite source (left empty)
}

// querier is the PostgreSQL read surface shared by *pgxpool.Pool and pgx.Tx, so
// the table list can be read outside a transaction (dry run) or inside one (real
// load).
type querier interface {
	Query(ctx context.Context, query string, args ...any) (pgx.Rows, error)
}

// MigrateSQLite copies every row from the legacy SQLite database at sqlitePath
// into the already-migrated PostgreSQL schema behind pg. The destination schema
// is the source of truth for what to copy: each PostgreSQL base table pulls from
// the same-named SQLite table, so SQLite's FTS5 shadow tables (absent in
// PostgreSQL) are skipped automatically, as are generated columns (tsvector). A
// target table with no SQLite source is left empty and listed in Skipped.
//
// dryRun previews the work — each target table's source row count, plus the
// no-source tables — without touching the destination. A real run does
// everything in one transaction, so a failure leaves the destination unchanged,
// and a re-run truncates and reloads to the same state (idempotent), then
// verifies every copied count actually persisted.
//
// Foreign-key triggers are disabled for the transaction with SET LOCAL
// session_replication_role (auto-reset at commit/rollback, so nothing leaks back
// to the pool), which lets tables load in any order without a topological sort.
// This is a deliberate faithful copy: SQLite does not enforce foreign keys, so
// any pre-existing orphan row is carried over as-is rather than failing the
// migration. Disabling the role requires a superuser DSN (the managed embedded
// cluster runs as one).
func MigrateSQLite(ctx context.Context, sqlitePath string, pg *pgxpool.Pool, dryRun bool) (*MigrateReport, error) {
	src, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", sqlitePath, err)
	}
	defer func() { _ = src.Close() }()
	if err := src.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", sqlitePath, err)
	}
	sqliteTables, err := sqliteTableSet(ctx, src)
	if err != nil {
		return nil, err
	}

	if dryRun {
		return migratePlan(ctx, src, pg, sqliteTables)
	}

	tx, err := pg.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// SET LOCAL scopes the role to this transaction: it reverts on commit or
	// rollback, so the connection returns to the pool with foreign-key triggers
	// re-enabled — no leak to later writers.
	if _, err := tx.Exec(ctx, "SET LOCAL session_replication_role = replica"); err != nil {
		return nil, fmt.Errorf("disable fk triggers: %w", err)
	}

	tables, err := pgBaseTables(ctx, tx)
	if err != nil {
		return nil, err
	}

	// RESTRICT, not CASCADE: every public table is in the list, so internal
	// foreign keys need no cascade. An unexpected reference from another schema
	// then fails loudly here instead of silently truncating that schema's data.
	if _, err := tx.Exec(ctx, "TRUNCATE "+strings.Join(quoteIdents(tables), ", ")+" RESTRICT"); err != nil {
		return nil, fmt.Errorf("truncate target tables: %w", err)
	}

	report := &MigrateReport{Tables: make(map[string]int, len(tables))}
	for _, t := range tables {
		if !sqliteTables[t] {
			report.Skipped = append(report.Skipped, t)
			continue
		}
		s, err := copyTable(ctx, src, tx, t)
		if err != nil {
			return nil, fmt.Errorf("copy %s: %w", t, err)
		}
		report.Tables[t] = s.rows
		report.Total += s.rows
		report.Sanitized += s.sanitized
		report.Converted += s.converted
		report.Dropped += s.dropped
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	// Cross-engine value checksums are deliberately not used: SQLite and
	// PostgreSQL render the same datum differently (0/1 vs bool, naive vs zoned
	// timestamps), so a value hash flags representation, not corruption. Integrity
	// instead rests on the atomic cast — any unconvertible value aborts the whole
	// transaction — plus count parity: re-count each table and confirm every
	// copied row actually persisted.
	for t, n := range report.Tables {
		var got int
		if err := pg.QueryRow(ctx, "SELECT count(*) FROM "+quoteIdent(t)).Scan(&got); err != nil {
			return nil, fmt.Errorf("verify %s: %w", t, err)
		}
		if got != n {
			return nil, fmt.Errorf("verify %s: copied %d rows but %d persisted", t, n, got)
		}
	}
	return report, nil
}

// migratePlan builds the dry-run report: every target table's SQLite source row
// count, with no-source tables listed in Skipped. It only reads, so the
// destination is untouched.
func migratePlan(ctx context.Context, src *sql.DB, pg querier, sqliteTables map[string]bool) (*MigrateReport, error) {
	tables, err := pgBaseTables(ctx, pg)
	if err != nil {
		return nil, err
	}
	report := &MigrateReport{Tables: make(map[string]int, len(tables))}
	for _, t := range tables {
		if !sqliteTables[t] {
			report.Skipped = append(report.Skipped, t)
			continue
		}
		var n int
		if err := src.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(t)).Scan(&n); err != nil {
			return nil, fmt.Errorf("count %s: %w", t, err)
		}
		report.Tables[t] = n
		report.Total += n
	}
	return report, nil
}

// sqliteTableSet returns the names of every table in the SQLite database, used to
// tell a PostgreSQL-only table (left empty) apart from a missing source.
func sqliteTableSet(ctx context.Context, src *sql.DB) (map[string]bool, error) {
	rows, err := src.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table'")
	if err != nil {
		return nil, fmt.Errorf("list sqlite tables: %w", err)
	}
	defer func() { _ = rows.Close() }()
	set := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		set[name] = true
	}
	return set, rows.Err()
}

// pgBaseTables lists the public tables to load, excluding goose_db_version
// (owned by the migration runner) and river_* (owned by River's own migrator and
// holding live queue state). Neither has a SQLite source, so including them would
// only TRUNCATE them — wiping the durable job queue on every SQLite import.
func pgBaseTables(ctx context.Context, q querier) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT tablename FROM pg_tables
		WHERE schemaname = 'public'
		  AND tablename <> 'goose_db_version'
		  AND tablename NOT LIKE 'river\_%' ESCAPE '\'
		ORDER BY tablename`)
	if err != nil {
		return nil, fmt.Errorf("list pg tables: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type pgColumn struct {
	name string
	udt  string // underlying type name, e.g. bool, timestamptz, int4, jsonb, bytea
}

// copyStats reports one table's copy outcome.
type copyStats struct {
	rows      int // rows inserted
	sanitized int // values whose invalid UTF-8 was replaced
	converted int // uuid values salvaged from a legacy-prefixed string
	dropped   int // rows skipped: a uuid column held an unsalvageable value
}

func copyTable(ctx context.Context, src *sql.DB, tx pgx.Tx, table string) (stats copyStats, err error) {
	pgCols, err := pgColumns(ctx, tx, table)
	if err != nil {
		return stats, err
	}
	srcCols, err := sqliteColumns(ctx, src, table)
	if err != nil {
		return stats, err
	}

	// Copy only the columns both sides share: a non-generated PostgreSQL column
	// also present in SQLite. PostgreSQL-only columns keep their defaults.
	var cols []pgColumn
	for _, c := range pgCols {
		if srcCols[c.name] {
			cols = append(cols, c)
		}
	}
	if len(cols) == 0 {
		return stats, nil
	}

	names := make([]string, len(cols))
	placeholders := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
		// $N::text::<udt>: bind every value as text and let PostgreSQL cast it to
		// the column type. This handles SQLite's loose storage uniformly — integer
		// 0/1 → boolean, naive or RFC3339 strings → timestamptz (the session is
		// UTC), JSON text → jsonb, hex string → bytea — with no per-type Go
		// conversion. The leading ::text keeps the bound parameter's type text, so
		// pgx sends a string and never expects an already-typed Go value.
		placeholders[i] = fmt.Sprintf("$%d::text::%s", i+1, c.udt)
	}

	// Legacy SQLite rows can hold a uuid wrapped in extra text in a now-uuid
	// column (the 'legacy-task-conversation-<uuid>' ids a removed task system
	// wrote into ctx_conversation.id). Rather than abort the cast or drop the
	// row, the inner uuid is extracted below and the original value rewritten to
	// it; the same rule runs on every uuid column, so a primary key and the
	// foreign keys referencing it all resolve to the identical uuid and still
	// join. Only a value with no recoverable uuid anywhere is dropped — a generic
	// safety net, expected to be zero for known data.
	where := uuidFilter(cols)
	query := "SELECT " + strings.Join(quoteIdents(names), ", ") + " FROM " + quoteIdent(table)
	if where != "" {
		query += " WHERE " + where
		if stats.dropped, err = countDropped(ctx, src, table, where); err != nil {
			return stats, err
		}
	}

	srcRows, err := src.QueryContext(ctx, query)
	if err != nil {
		return stats, fmt.Errorf("read sqlite: %w", err)
	}
	defer func() { _ = srcRows.Close() }()

	// pgx prepares and caches this INSERT on first Exec and reuses the plan for
	// every later row (default statement-cache mode), so a per-row Exec is as
	// cheap as the explicit prepared statement this replaces.
	insertSQL := "INSERT INTO " + quoteIdent(table) + " (" + strings.Join(quoteIdents(names), ", ") +
		") VALUES (" + strings.Join(placeholders, ", ") + ")"

	for srcRows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := srcRows.Scan(ptrs...); err != nil {
			return stats, fmt.Errorf("scan row: %w", err)
		}
		args := make([]any, len(cols))
		for i := range cols {
			val, fixed := toText(raw[i], cols[i].udt)
			if fixed {
				stats.sanitized++
			}
			if cols[i].udt == "uuid" {
				if s, ok := val.(string); ok {
					if u := extractUUID(s); u != "" && u != s {
						val = u
						stats.converted++
					}
				}
			}
			args[i] = val
		}
		if _, err := tx.Exec(ctx, insertSQL, args...); err != nil {
			return stats, fmt.Errorf("insert row %d: %w", stats.rows+1, err)
		}
		stats.rows++
	}
	return stats, srcRows.Err()
}

// pgColumns returns the writable (non-generated) columns of a public table in
// ordinal order, each with its underlying type name for the cast.
func pgColumns(ctx context.Context, tx pgx.Tx, table string) ([]pgColumn, error) {
	rows, err := tx.Query(ctx, `
		SELECT column_name, udt_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND is_generated = 'NEVER'
		ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("read pg columns: %w", err)
	}
	defer rows.Close()
	var cols []pgColumn
	for rows.Next() {
		var c pgColumn
		if err := rows.Scan(&c.name, &c.udt); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// sqliteColumns returns the set of column names on a SQLite table, or an empty
// set when the table does not exist. The table name comes from the PostgreSQL
// catalog, so interpolating it into PRAGMA (which cannot bind parameters) is safe.
func sqliteColumns(ctx context.Context, src *sql.DB, table string) (map[string]bool, error) {
	rows, err := src.QueryContext(ctx, "PRAGMA table_info("+quoteIdent(table)+")")
	if err != nil {
		return nil, fmt.Errorf("read sqlite columns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	set := make(map[string]bool)
	for rows.Next() {
		var (
			cid, notnull, pk int
			name, typ        string
			dflt             any
		)
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		set[name] = true
	}
	return set, rows.Err()
}

// toText renders a SQLite-scanned value as the text PostgreSQL will cast to the
// column type, or nil for SQL NULL, and reports whether invalid UTF-8 was
// replaced. modernc/sqlite yields int64, float64, string, []byte, or nil.
func toText(v any, udt string) (any, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case int64:
		return strconv.FormatInt(x, 10), false
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), false
	case string:
		return cleanUTF8(x)
	case []byte:
		if udt == "bytea" {
			// PostgreSQL hex input format: \x followed by the bytes in hex.
			return `\x` + hex.EncodeToString(x), false
		}
		return cleanUTF8(string(x))
	default:
		return fmt.Sprint(x), false
	}
}

// cleanUTF8 replaces invalid UTF-8 byte sequences with the Unicode replacement
// character, reporting whether it changed anything. SQLite does not enforce text
// encoding, so a legacy database can hold bytes — often a multibyte character
// truncated by a length cap — that PostgreSQL's UTF8 encoding rejects; sanitizing
// keeps one bad byte from aborting the whole migration. The report's Sanitized
// count surfaces how many values were touched so they can be reviewed.
func cleanUTF8(s string) (string, bool) {
	if utf8.ValidString(s) {
		return s, false
	}
	return strings.ToValidUTF8(s, "�"), true
}

// uuidGlob matches the hyphenated 8-4-4-4-12 uuid form inside a SQLite GLOB.
const uuidGlob = "[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]-" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f]-" +
	"[0-9a-f][0-9a-f][0-9a-f][0-9a-f]-[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]"

// uuidRE extracts the first hyphenated uuid embedded in a string, so a legacy
// 'legacy-task-conversation-<uuid>' value yields its inner uuid.
var uuidRE = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// extractUUID returns the uuid embedded in s (the whole string when s already is
// one), or "" when s carries no hyphenated uuid. The 32-char unhyphenated form
// PostgreSQL also casts is intentionally left untouched (returns "" so the
// caller keeps the original).
func extractUUID(s string) string {
	return uuidRE.FindString(s)
}

// uuidFilter builds a SQLite WHERE expression that keeps a row only when every
// uuid column is NULL, a castable uuid, or carries one embedded in extra text
// (recoverable by extractUUID). It returns "" when the table has no uuid column.
// lower() folds case; the 32-char unhyphenated form PostgreSQL also accepts is
// matched directly. A value with no recoverable uuid matches nothing and drops.
func uuidFilter(cols []pgColumn) string {
	plain := strings.Repeat("[0-9a-f]", 32)
	var clauses []string
	for _, c := range cols {
		if c.udt != "uuid" {
			continue
		}
		q := quoteIdent(c.name)
		clauses = append(clauses, fmt.Sprintf("(%s IS NULL OR lower(%s) GLOB '*%s*' OR lower(%s) GLOB '%s')", q, q, uuidGlob, q, plain))
	}
	return strings.Join(clauses, " AND ")
}

// countDropped reports how many SQLite rows the uuid filter excludes, so the
// migration can surface dropped legacy rows instead of hiding them.
func countDropped(ctx context.Context, src *sql.DB, table, where string) (int, error) {
	var n int
	if err := src.QueryRowContext(ctx, "SELECT count(*) FROM "+quoteIdent(table)+" WHERE NOT ("+where+")").Scan(&n); err != nil {
		return 0, fmt.Errorf("count dropped %s: %w", table, err)
	}
	return n, nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func quoteIdents(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quoteIdent(n)
	}
	return out
}
