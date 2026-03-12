package db

import "embed"

//go:embed schemas/tables/*.sql
var SchemaFS embed.FS

// MigrationsFS holds Atlas migration files. Embed the entire directory
// so it works even when no migrations exist yet.
//
//go:embed all:migrations
var MigrationsFS embed.FS
