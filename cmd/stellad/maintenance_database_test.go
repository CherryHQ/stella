package main

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestMaintenanceDatabaseOpenerUsesBoundedUTCPool(t *testing.T) {
	db := dbtest.New(t)
	var observed bool
	app := &ucli.App{
		Commands: []*ucli.Command{{
			Name: "open",
			Action: func(c *ucli.Context) error {
				return withMaintenanceDatabaseConfig(c, config.DatabaseConfig{URL: db.Config().ConnString()}, "stellad-maintenance-test", func(_ context.Context, pool *pgxpool.Pool) error {
					cfg := pool.Config()
					if cfg.MaxConns != 2 || cfg.MinConns != 0 {
						t.Errorf("pool bounds = (%d, %d), want (2, 0)", cfg.MaxConns, cfg.MinConns)
					}
					if cfg.ConnConfig.RuntimeParams["timezone"] != "UTC" {
						t.Errorf("timezone = %q, want UTC", cfg.ConnConfig.RuntimeParams["timezone"])
					}
					if cfg.ConnConfig.RuntimeParams["application_name"] != "stellad-maintenance-test" {
						t.Errorf("application_name = %q", cfg.ConnConfig.RuntimeParams["application_name"])
					}
					observed = true
					return nil
				})
			},
		}},
	}
	if err := app.RunContext(t.Context(), []string{"stellad", "open"}); err != nil {
		t.Fatalf("open maintenance database: %v", err)
	}
	if !observed {
		t.Fatal("maintenance action did not run")
	}
}

func TestMaintenanceDatabaseRejectsEmbeddedWhenExternalRequired(t *testing.T) {
	app := &ucli.App{
		Commands: []*ucli.Command{{
			Name: "open",
			Action: func(c *ucli.Context) error {
				return withMaintenanceDatabaseConfig(c, config.DatabaseConfig{RequireExternalDB: true}, "stellad-maintenance-test", func(context.Context, *pgxpool.Pool) error {
					t.Fatal("maintenance action ran with embedded database forbidden")
					return nil
				})
			},
		}},
	}
	err := app.RunContext(t.Context(), []string{"stellad", "open"})
	if err == nil || !strings.Contains(err.Error(), "STELLA_DATABASE_URL is required") {
		t.Fatalf("error = %v, want strict external database guard", err)
	}
}
