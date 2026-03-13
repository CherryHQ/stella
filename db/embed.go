package db

import "embed"

// MigrationsFS holds Atlas migration files. Embed the entire directory
// so it works even when no migrations exist yet.
//
//go:embed all:migrations
var MigrationsFS embed.FS
