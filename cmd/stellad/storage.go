package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/blob"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/home"
)

type (
	retryPurgeFunc    func(context.Context, string, string) (home.Record, error)
	migrateAssetsFunc func(context.Context, string, bool) (home.MutableAssetMigrationSummary, error)
)

func storageCommand() *ucli.Command {
	return &ucli.Command{
		Name: "storage", Usage: "Maintain durable storage", Category: "Admin",
		Subcommands: []*ucli.Command{retryPurgeCommand(retryFailedPurge), migrateAssetsCommand(migrateAssets)},
	}
}

func migrateAssetsCommand(migrate migrateAssetsFunc) *ucli.Command {
	return &ucli.Command{
		Name: "migrate-assets", Usage: "Copy legacy mutable assets into PrincipalHomes",
		Description: "Copies mutable assets from the legacy object authority without deleting remote objects. Before a non-dry run, stop all old binaries, services, pods, and jobs that can write assets; retain the old STELLA_BLOB_S3_* configuration until completion.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "database-url", Usage: "PostgreSQL URL (overrides STELLA_DATABASE_URL)"},
			&ucli.BoolFlag{Name: "dry-run", Usage: "Verify and report the planned migration without writing Homes or the marker"},
			&ucli.BoolFlag{Name: "json", Usage: "Emit JSON"},
			&ucli.BoolFlag{Name: "confirm-writers-stopped", Usage: "Confirm all old binaries, services, pods, and jobs are stopped"},
		},
		Action: migrateAssetsAction(migrate),
	}
}

func migrateAssetsAction(migrate migrateAssetsFunc) ucli.ActionFunc {
	return func(c *ucli.Context) error {
		if !c.IsSet("confirm-writers-stopped") || !c.Bool("confirm-writers-stopped") {
			return errors.New("storage migrate-assets: --confirm-writers-stopped is required; stop all old binaries, services, pods, and jobs first")
		}
		if c.Bool("dry-run") {
			_, _ = fmt.Fprintln(c.App.ErrWriter, "Verifying legacy mutable assets...")
		} else {
			_, _ = fmt.Fprintln(c.App.ErrWriter, "Migrating legacy mutable assets...")
		}
		summary, err := migrate(c.Context, c.String("database-url"), c.Bool("dry-run"))
		if err != nil {
			return fmt.Errorf("storage migrate-assets: %w", err)
		}
		if c.Bool("json") {
			return json.NewEncoder(c.App.Writer).Encode(summary)
		}
		_, err = fmt.Fprintf(c.App.Writer, "%s\t%s\t%d\t%d\t%s\n", summary.Status, summary.MarkerState, summary.Count, summary.Bytes, summary.SHA256)
		return err
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
	store, err := home.NewLocalStore(cfg.HomeStoreID, config.StellaHome())
	if err != nil {
		return home.Record{}, fmt.Errorf("configure Home Store: %w", err)
	}
	registry, err := home.NewRegistry(db, cfg.HomeStoreID, store)
	if err != nil {
		return home.Record{}, fmt.Errorf("build Home registry: %w", err)
	}
	return registry.RetryFailedPurge(ctx, id, "stellad storage retry-purge")
}

func migrateAssets(ctx context.Context, databaseURL string, dryRun bool) (home.MutableAssetMigrationSummary, error) {
	cfg, err := config.LoadAssetMigrationConfig(os.LookupEnv)
	if err != nil {
		return home.MutableAssetMigrationSummary{}, fmt.Errorf("load configuration: %w", err)
	}
	dsn := databaseURL
	if dsn == "" {
		dsn = cfg.DatabaseURL
	}
	var embedded *appdb.Embedded
	if dsn == "" {
		embedded, err = appdb.StartEmbedded(filepath.Join(config.StellaHome(), "postgres"), 0)
		if err != nil {
			return home.MutableAssetMigrationSummary{}, fmt.Errorf("start embedded postgres: %w", err)
		}
		defer func() { _ = embedded.Stop() }()
		dsn = embedded.DSN()
	}
	db, err := appdb.OpenDB(dsn)
	if err != nil {
		return home.MutableAssetMigrationSummary{}, fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	store, err := home.NewLocalStore(cfg.HomeStoreID, config.StellaHome())
	if err != nil {
		return home.MutableAssetMigrationSummary{}, fmt.Errorf("configure Home Store: %w", err)
	}
	registry, err := home.NewRegistry(db, cfg.HomeStoreID, store)
	if err != nil {
		return home.MutableAssetMigrationSummary{}, fmt.Errorf("build Home registry: %w", err)
	}
	source, err := blob.NewStoreFromConfig(cfg.Blob)
	if err != nil {
		return home.MutableAssetMigrationSummary{}, fmt.Errorf("configure legacy blob authority: %w", err)
	}
	if source == nil {
		return home.MutableAssetMigrationSummary{}, errors.New("legacy STELLA_BLOB_S3_* object authority is required")
	}
	return registry.MigrateMutableAssets(ctx, source, home.MutableAssetMigrationOptions{DryRun: dryRun})
}
