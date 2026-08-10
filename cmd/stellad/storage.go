package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/home"
)

type retryPurgeFunc func(context.Context, string, string) (home.Record, error)

func storageCommand() *ucli.Command {
	return &ucli.Command{
		Name: "storage", Usage: "Maintain durable storage", Category: "Admin",
		Subcommands: []*ucli.Command{retryPurgeCommand(retryFailedPurge)},
	}
}

func retryPurgeCommand(retry retryPurgeFunc) *ucli.Command {
	return &ucli.Command{
		Name: "retry-purge", Usage: "Retry one failed Home physical purge", ArgsUsage: "<home-id>",
		Description: "Retries exactly one Home whose physical purge was durably recorded as purge_failed. It never purges ready or tombstoned Homes.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "database-url", Usage: "PostgreSQL URL (overrides STELLA_DATABASE_URL)"},
			&ucli.BoolFlag{Name: "json", Usage: "Emit JSON"},
		},
		Action: retryPurgeAction(retry),
	}
}

func retryPurgeAction(retry retryPurgeFunc) ucli.ActionFunc {
	return func(c *ucli.Context) error {
		if c.NArg() != 1 || c.Args().First() == "" {
			return fmt.Errorf("storage retry-purge: home ID is required")
		}
		record, err := retry(c.Context, c.Args().First(), c.String("database-url"))
		if err != nil {
			return fmt.Errorf("storage retry-purge: %w", err)
		}
		if c.Bool("json") {
			return json.NewEncoder(c.App.Writer).Encode(struct {
				HomeID string `json:"home_id"`
				State  string `json:"state"`
			}{record.ID, string(record.State)})
		}
		_, err = fmt.Fprintf(c.App.Writer, "%s\t%s\n", record.ID, record.State)
		return err
	}
}

func retryFailedPurge(ctx context.Context, id, databaseURL string) (home.Record, error) {
	cfg, err := config.LoadHomeMaintenanceConfig(os.LookupEnv)
	if err != nil {
		return home.Record{}, fmt.Errorf("load configuration: %w", err)
	}
	dsn := databaseURL
	if dsn == "" {
		dsn = cfg.DatabaseURL
	}
	var embedded *appdb.Embedded
	if dsn == "" {
		embedded, err = appdb.StartEmbedded(filepath.Join(config.StellaHome(), "postgres"), 0)
		if err != nil {
			return home.Record{}, fmt.Errorf("start embedded postgres: %w", err)
		}
		defer func() { _ = embedded.Stop() }()
		dsn = embedded.DSN()
	}
	db, err := appdb.OpenDB(dsn)
	if err != nil {
		return home.Record{}, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	registry, err := home.NewLocalRegistry(ctx, db, cfg.HomeStoreID, config.StellaHome())
	if err != nil {
		return home.Record{}, fmt.Errorf("build Home registry: %w", err)
	}
	return registry.RetryFailedPurge(ctx, id, "stellad storage retry-purge")
}
