package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/scheduler"
)

func schedulerCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "scheduler",
		Usage: "Manage scheduled tasks",
		Subcommands: []*ucli.Command{
			schedulerAddCommand(),
			schedulerListCommand(),
			schedulerRemoveCommand(),
		},
	}
}

// resolveUserAndAgent authenticates via ANNA_TOKEN and returns the user ID plus
// the agent ID cryptographically bound to the token (empty for user-level tokens).
func resolveUserAndAgent(ctx context.Context, svc *auth.TokenService) (int64, string, error) {
	token := os.Getenv("ANNA_TOKEN")
	if token == "" {
		return 0, "", fmt.Errorf("ANNA_TOKEN env var is required")
	}
	user, agentID, err := svc.AuthenticateWithAgent(ctx, token)
	if err != nil {
		return 0, "", fmt.Errorf("ANNA_TOKEN authentication failed: %w", err)
	}
	return user.ID, agentID, nil
}

// openScheduler opens the DB, authenticates via ANNA_TOKEN, and returns a ready Store.
func openScheduler(c *ucli.Context) (*scheduler.Store, int64, string, func(), error) {
	rawDB, err := db.OpenDB(config.DBPath())
	if err != nil {
		return nil, 0, "", nil, fmt.Errorf("open database: %w", err)
	}
	authStore := db.NewAuthStore(rawDB)
	tokenSvc := auth.NewTokenService(authStore, nil)
	userID, agentID, err := resolveUserAndAgent(c.Context, tokenSvc)
	if err != nil {
		_ = rawDB.Close()
		return nil, 0, "", nil, err
	}
	return scheduler.NewStore(rawDB), userID, agentID, func() { _ = rawDB.Close() }, nil
}

func schedulerAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "add",
		Usage: "Add a scheduled job",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "name", Usage: "Job name (required)", Required: true},
			&ucli.StringFlag{Name: "message", Usage: "Prompt to execute on schedule (required)", Required: true},
			&ucli.StringFlag{Name: "cron", Usage: "Cron expression, e.g. '0 9 * * 1-5'"},
			&ucli.StringFlag{Name: "every", Usage: "Go duration interval, e.g. '30m', '1h'"},
			&ucli.StringFlag{Name: "at", Usage: "RFC3339 one-time timestamp, e.g. '2024-01-15T14:30:00+08:00'"},
			&ucli.StringFlag{Name: "session-mode", Usage: "'reuse' (default) or 'new'", Value: scheduler.SessionReuse},
		},
		Action: func(c *ucli.Context) error {
			store, userID, agentID, close, err := openScheduler(c)
			if err != nil {
				return err
			}
			defer close()

			job, err := store.AddJob(c.Context, scheduler.AddJobParams{
				Name:        c.String("name"),
				Message:     c.String("message"),
				Schedule:    scheduler.Schedule{Cron: c.String("cron"), Every: c.String("every"), At: c.String("at")},
				SessionMode: c.String("session-mode"),
				UserID:      userID,
				AgentID:     agentID,
			})
			if err != nil {
				return err
			}
			out, _ := json.MarshalIndent(job, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
}

func schedulerListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List scheduled jobs",
		Action: func(c *ucli.Context) error {
			store, userID, _, close, err := openScheduler(c)
			if err != nil {
				return err
			}
			defer close()

			jobs, err := store.ListUserJobs(c.Context, userID)
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				fmt.Println("[]")
				return nil
			}
			out, _ := json.MarshalIndent(jobs, "", "  ")
			fmt.Println(string(out))
			return nil
		},
	}
}

func schedulerRemoveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "remove",
		Usage:     "Remove a scheduled job",
		ArgsUsage: "<job-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: anna scheduler remove <job-id>")
			}
			store, userID, _, close, err := openScheduler(c)
			if err != nil {
				return err
			}
			defer close()

			if err := store.RemoveJob(c.Context, id, userID); err != nil {
				return err
			}
			fmt.Printf("Job %q removed.\n", id)
			return nil
		},
	}
}
