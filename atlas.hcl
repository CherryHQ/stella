// Atlas dev database for diffing the PostgreSQL schema.
//
// It must be a real PostgreSQL instance, not the SQLite in-memory dev of old:
// the schema declares TIMESTAMPTZ/BOOLEAN/BYTEA/JSONB types and tsvector
// generated columns whose Go types depend on a true PostgreSQL diff.
//
// The dev database MUST have the pg_trgm extension available, because three
// indexes use the gin_trgm_ops operator class (the CJK/substring FTS fallback).
// pg_trgm is created at runtime by ensureFTS, not by Atlas, so it cannot live in
// the schema source; seed it once in the dev database:
//   CREATE EXTENSION IF NOT EXISTS pg_trgm;
// Atlas's docker:// driver and the gated docker{} block cannot seed it on OSS,
// so dev_url points at a pre-seeded PostgreSQL. Override per environment, e.g.:
//   atlas migrate diff <name> --env local --var dev_url="postgres://...?sslmode=disable"
variable "dev_url" {
  type    = string
  default = "postgres://postgres:dev@localhost:55542/postgres?search_path=public&sslmode=disable"
}

env "local" {
  src = "file://internal/db/schemas/main.sql"
  dev = var.dev_url

  migration {
    dir = "file://internal/db/migrations"
  }
}
