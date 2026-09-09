package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	ucli "github.com/urfave/cli/v2"

	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/platform/config"
)

const maintenanceDatabaseTimeout = 2 * time.Minute

// withMaintenanceDatabase loads the narrow maintenance configuration and
// opens an existing database without migrations or server workers. The pool is
// deliberately capped at two connections: these commands are single-operator
// maintenance paths, not a second application server.
func withMaintenanceDatabase(c *ucli.Context, applicationName string, action func(context.Context, *pgxpool.Pool) error) error {
	cfg, err := config.LoadMaintenanceConfig(os.LookupEnv, false)
	if err != nil {
		return fmt.Errorf("load maintenance database configuration: %w", err)
	}
	return withMaintenanceDatabaseConfig(c, cfg.Database, applicationName, action)
}

func withMaintenanceDatabaseConfig(c *ucli.Context, database config.DatabaseConfig, applicationName string, action func(context.Context, *pgxpool.Pool) error) (resultErr error) {
	ctx, cancel := context.WithTimeout(c.Context, maintenanceDatabaseTimeout)
	defer cancel()

	dsn := database.URL
	var embedded *appdb.Embedded
	if dsn == "" {
		if database.RequireExternalDB {
			return errors.New("STELLA_DATABASE_URL is required when STELLA_REQUIRE_EXTERNAL_DB is enabled")
		}
		dataDir := filepath.Join(config.StellaHome(), "postgres")
		if err := requireStoppedMaintenanceDatabase(dataDir); err != nil {
			return err
		}
		var err error
		embedded, err = appdb.StartEmbedded(dataDir, 0)
		if err != nil {
			return fmt.Errorf("open stopped embedded database: %w", err)
		}
		defer func() { resultErr = errors.Join(resultErr, embedded.Stop()) }()
		dsn = embedded.DSN()
	}

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid maintenance database connection configuration")
	}
	poolConfig.MaxConns = 2
	poolConfig.MinConns = 0
	poolConfig.ConnConfig.ConnectTimeout = 10 * time.Second
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open maintenance database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect to maintenance database: %w", err)
	}
	return action(ctx, pool)
}

func requireStoppedMaintenanceDatabase(dataDir string) error {
	version, err := os.Stat(filepath.Join(dataDir, "PG_VERSION"))
	if err != nil || !version.Mode().IsRegular() {
		return errors.New("no existing embedded database; set STELLA_DATABASE_URL or use the initialized STELLA_HOME")
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("embedded database may be running; stop stellad before maintenance")
	}
	return nil
}
