package db

import "database/sql"

// ensureFTS is a placeholder during the SQLite→PostgreSQL migration.
//
// The SQLite implementation created fts5 virtual tables plus sync triggers and
// backfilled them at startup. PostgreSQL full-text search is built differently:
// the searchable columns are GENERATED tsvector columns defined directly in the
// ctx_message, ctx_summary, and recally_article table schemas, each backed by a
// GIN index — so there are no virtual tables or triggers to maintain at runtime.
// The only piece Atlas (OSS) cannot manage declaratively is the pg_trgm
// extension, which must exist before the schema is applied.
//
// TODO(Phase 5): give this a PostgreSQL body that ensures the pg_trgm extension
// exists, and move the OpenDB call site to run before Atlas apply rather than
// after migrations succeed.
func ensureFTS(_ *sql.DB) error {
	return nil
}
