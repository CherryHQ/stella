package main

import (
	"fmt"
	"path/filepath"
	"strings"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
)

func dbCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "db",
		Usage: "Database maintenance",
		Subcommands: []*ucli.Command{
			migrateSQLiteCommand(),
		},
	}
}

func migrateSQLiteCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "migrate-sqlite",
		Usage: "Copy a legacy SQLite database into PostgreSQL",
		Description: `Loads every row from the old SQLite database into PostgreSQL.

Run this once, with the server stopped, when upgrading from a SQLite-backed
release. The target schema is created automatically. When STELLA_DATABASE_URL is
unset, the managed embedded PostgreSQL is used — the same cluster the server runs
on — so the server picks up the data on its next start.

The load is idempotent: every target table is truncated before copying, so a
re-run after a failure is safe.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:  "sqlite",
				Usage: "path to the source SQLite database",
				Value: config.DBPath(),
			},
		},
		Action: func(c *ucli.Context) error {
			sqlitePath := c.String("sqlite")

			// Mirror the server's selection: an explicit DSN targets external
			// PostgreSQL, otherwise migrate into the managed embedded cluster.
			dsn := config.DatabaseURL()
			if dsn == "" {
				emb, err := appdb.StartEmbedded(filepath.Join(config.StellaHome(), "postgres"), 0)
				if err != nil {
					return fmt.Errorf("start embedded postgres: %w", err)
				}
				defer func() { _ = emb.Stop() }()
				dsn = emb.DSN()
			}

			pg, err := appdb.OpenDB(dsn)
			if err != nil {
				return fmt.Errorf("open postgres: %w", err)
			}
			defer func() { _ = pg.Close() }()

			fmt.Printf("Migrating %s -> PostgreSQL...\n", sqlitePath)
			report, err := appdb.MigrateSQLite(c.Context, sqlitePath, pg)
			if err != nil {
				return err
			}
			fmt.Printf("Done: copied %d rows across %d tables.\n", report.Total, len(report.Tables))
			if report.Sanitized > 0 {
				fmt.Printf("Note: replaced invalid UTF-8 in %d value(s); review affected rows.\n", report.Sanitized)
			}
			if len(report.Skipped) > 0 {
				fmt.Printf("Note: %d table(s) had no SQLite source and were left empty: %s\n",
					len(report.Skipped), strings.Join(report.Skipped, ", "))
			}
			return nil
		},
	}
}
