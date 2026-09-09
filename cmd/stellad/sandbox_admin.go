package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	ucli "github.com/urfave/cli/v2"

	agentsandbox "github.com/CherryHQ/stella/internal/agent/sandbox"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/platform/config"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

func sandboxCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "sandbox",
		Usage:    "Inspect and recover fenced sandbox resources",
		Category: "System",
		Description: "Use STELLA_DATABASE_URL for an external database. With embedded PostgreSQL, " +
			"stop stellad first; these commands temporarily open the existing database under STELLA_HOME. " +
			"They do not start agents, create a database, or apply migrations. Resource recovery never replays a turn.",
		Subcommands: []*ucli.Command{
			{
				Name:      "inspect",
				Usage:     "Inspect the current sandbox generation and its resource identity",
				ArgsUsage: "<session-id>",
				Flags:     []ucli.Flag{&ucli.BoolFlag{Name: "json", Usage: "Emit the durable record as JSON"}},
				Action: func(c *ucli.Context) error {
					if err := validateSandboxTarget(c, false); err != nil {
						return err
					}
					return withSandboxAdmin(c, false, func(ctx context.Context, admin *agentsandbox.GenerationAdmin) error {
						row, err := admin.Inspect(ctx, c.Args().First())
						if err != nil {
							return err
						}
						return writeSandboxRecord(c.App.Writer, row, c.Bool("json"))
					})
				},
			},
			{
				Name:      "reconcile",
				Usage:     "Terminate an exact fenced generation and verify resource absence",
				ArgsUsage: "<session-id>",
				Description: "Only fenced or unknown generations can be reconciled. The backend must prove " +
					"that the recorded resource has stopped in the recorded control domain. Unreachable control " +
					"planes and missing termination evidence leave the generation blocked. Example: " +
					"stellad sandbox reconcile --generation 3 <session-id>",
				Flags: []ucli.Flag{
					&ucli.Int64Flag{Name: "generation", Required: true, Usage: "Exact generation reported by inspect"},
					&ucli.BoolFlag{Name: "json", Usage: "Emit the resulting durable record as JSON"},
				},
				Action: func(c *ucli.Context) error {
					if err := validateSandboxTarget(c, true); err != nil {
						return err
					}
					return withSandboxAdmin(c, true, func(ctx context.Context, admin *agentsandbox.GenerationAdmin) error {
						row, observation, err := admin.Reconcile(ctx, c.Args().First(), c.Int64("generation"))
						if err != nil {
							return err
						}
						if observation.State != pkgsandbox.ResourceStateAbsent {
							return fmt.Errorf("sandbox generation %d remains blocked: %s", row.Generation, observation.Detail)
						}
						return writeSandboxRecord(c.App.Writer, row, c.Bool("json"))
					})
				},
			},
			{
				Name:      "acknowledge-absent",
				Usage:     "Record an operator's proof that an unknown generation has stopped",
				ArgsUsage: "<session-id>",
				Description: "This permits a fresh generation. It does not terminate processes or containers. " +
					"First stop the old executor and verify that all processes belonging to the recorded resource " +
					"have stopped, including detached children. Copy the exact generation and owner boot from inspect. " +
					"The reason is retained with the durable record. A live owner or running turn prevents acknowledgement. " +
					"Example: stellad sandbox acknowledge-absent --generation 3 --owner-boot <boot-id> " +
					"--reason 'Verified the old host was powered off' --force <session-id>",
				Flags: []ucli.Flag{
					&ucli.Int64Flag{Name: "generation", Required: true, Usage: "Exact unknown generation reported by inspect"},
					&ucli.StringFlag{Name: "owner-boot", Required: true, Usage: "Exact old executor boot ID reported by inspect"},
					&ucli.StringFlag{Name: "reason", Required: true, Usage: "Audited explanation of the resource termination proof"},
					&ucli.BoolFlag{Name: "force", Usage: "Confirm that the whole recorded resource has stopped"},
					&ucli.BoolFlag{Name: "json", Usage: "Emit the resulting durable record as JSON"},
				},
				Action: func(c *ucli.Context) error {
					if err := validateSandboxTarget(c, true); err != nil {
						return err
					}
					if _, err := uuid.Parse(c.String("owner-boot")); err != nil {
						return errors.New("--owner-boot must be the executor UUID reported by inspect")
					}
					if strings.TrimSpace(c.String("reason")) == "" || !c.Bool("force") {
						return errors.New("acknowledgement requires a non-empty --reason and explicit --force")
					}
					return withSandboxAdmin(c, false, func(ctx context.Context, admin *agentsandbox.GenerationAdmin) error {
						row, err := admin.AcknowledgeAbsent(ctx, c.Args().First(), c.Int64("generation"), c.String("owner-boot"), strings.TrimSpace(c.String("reason")), c.Bool("force"))
						if err != nil {
							return err
						}
						return writeSandboxRecord(c.App.Writer, row, c.Bool("json"))
					})
				},
			},
		},
	}
}

func validateSandboxTarget(c *ucli.Context, generationRequired bool) error {
	if c.Args().Len() != 1 || strings.TrimSpace(c.Args().First()) == "" {
		return errors.New("provide exactly one session ID")
	}
	if generationRequired && c.Int64("generation") <= 0 {
		return errors.New("--generation must be a positive number reported by inspect")
	}
	return nil
}

func withSandboxAdmin(c *ucli.Context, controllers bool, action func(context.Context, *agentsandbox.GenerationAdmin) error) (resultErr error) {
	ctx, cancel := context.WithTimeout(c.Context, 2*time.Minute)
	defer cancel()
	cfg, err := config.LoadSandboxMaintenanceConfig(os.LookupEnv, controllers)
	if err != nil {
		return fmt.Errorf("load sandbox maintenance configuration: %w", err)
	}
	dsn := cfg.Database.URL
	if dsn == "" {
		if cfg.Database.RequireExternalDB {
			return errors.New("STELLA_DATABASE_URL is required when STELLA_REQUIRE_EXTERNAL_DB is enabled")
		}
		dataDir := filepath.Join(config.StellaHome(), "postgres")
		if err := requireStoppedSandboxDatabase(dataDir); err != nil {
			return err
		}
		embedded, err := appdb.StartEmbedded(dataDir, 0)
		if err != nil {
			return fmt.Errorf("open stopped embedded database: %w", err)
		}
		defer func() { resultErr = errors.Join(resultErr, embedded.Stop()) }()
		dsn = embedded.DSN()
	}
	// OpenDB applies migrations. Maintenance must inspect existing state without
	// upgrading it or starting the server's execution and recovery services.
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid sandbox maintenance database connection configuration")
	}
	poolConfig.MaxConns = 2
	poolConfig.MinConns = 0
	poolConfig.ConnConfig.ConnectTimeout = 10 * time.Second
	poolConfig.ConnConfig.RuntimeParams["timezone"] = "UTC"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "stellad-sandbox-maintenance"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return fmt.Errorf("open sandbox maintenance database: %w", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		return fmt.Errorf("connect to sandbox maintenance database: %w", err)
	}
	var backends *agentsandbox.BackendRegistry
	if controllers {
		backends, err = setupSandboxBackends(ctx, config.ServerConfig{KubernetesSandbox: cfg.KubernetesSandbox})
		if err != nil {
			return fmt.Errorf("configure sandbox resource controllers: %w", err)
		}
	}
	return action(ctx, agentsandbox.NewGenerationAdmin(pool, backends))
}

func requireStoppedSandboxDatabase(dataDir string) error {
	version, err := os.Stat(filepath.Join(dataDir, "PG_VERSION"))
	if err != nil || !version.Mode().IsRegular() {
		return errors.New("no existing embedded database; set STELLA_DATABASE_URL or use the initialized STELLA_HOME")
	}
	if _, err := os.Lstat(filepath.Join(dataDir, "postmaster.pid")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("embedded database may be running; stop stellad before sandbox maintenance")
	}
	return nil
}

func writeSandboxRecord(out io.Writer, row agentsandbox.GenerationRecord, asJSON bool) error {
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(row)
	}
	_, err := fmt.Fprintf(out, "Session: %s\nGeneration: %d\nOwner boot: %s\nBackend: %s\nState: %s\nResource authority: %s\nResource reference: %s\nDetail: %s\nUpdated: %s\n",
		row.SessionID, row.Generation, row.OwnerBootID, row.Backend, row.State, row.Resource.Authority, row.Resource.Ref, row.LastError, row.UpdatedAt.UTC().Format(time.RFC3339))
	return err
}
