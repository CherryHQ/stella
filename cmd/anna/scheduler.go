package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/scheduler"
	"github.com/vaayne/anna/pkg/db/sqlc"
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

// openSchedulerDB opens the DB and authenticates via ANNA_TOKEN.
func openSchedulerDB(c *ucli.Context) (*sqlc.Queries, int64, *sql.DB, error) {
	dbPath := config.DBPath()
	rawDB, err := db.OpenDB(dbPath)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("open database: %w", err)
	}
	authStore := db.NewAuthStore(rawDB)
	tokenSvc := auth.NewTokenService(authStore, nil)
	userID, err := resolveUserID(c.Context, tokenSvc)
	if err != nil {
		_ = rawDB.Close()
		return nil, 0, nil, err
	}
	return sqlc.New(rawDB), userID, rawDB, nil
}

func schedulerAddCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "add",
		Usage: "Add a scheduled job",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:     "name",
				Usage:    "Job name (required)",
				Required: true,
			},
			&ucli.StringFlag{
				Name:     "message",
				Usage:    "Prompt to execute on schedule (required)",
				Required: true,
			},
			&ucli.StringFlag{
				Name:  "cron",
				Usage: "Cron expression, e.g. '0 9 * * 1-5' (use one of: cron, every, at)",
			},
			&ucli.StringFlag{
				Name:  "every",
				Usage: "Go duration interval, e.g. '30m', '1h', '24h' (use one of: cron, every, at)",
			},
			&ucli.StringFlag{
				Name:  "at",
				Usage: "RFC3339 one-time timestamp, e.g. '2024-01-15T14:30:00+08:00' (use one of: cron, every, at)",
			},
			&ucli.StringFlag{
				Name:  "session-mode",
				Usage: "Session behavior: 'reuse' (default) or 'new'",
				Value: scheduler.SessionReuse,
			},
		},
		Action: func(c *ucli.Context) error {
			q, userID, rawDB, err := openSchedulerDB(c)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := rawDB.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			cronExpr := c.String("cron")
			every := c.String("every")
			at := c.String("at")

			sched := scheduler.Schedule{Cron: cronExpr, Every: every, At: at}
			if err := validateScheduleArgs(sched); err != nil {
				return err
			}

			sessionMode := c.String("session-mode")
			if sessionMode != scheduler.SessionReuse && sessionMode != scheduler.SessionNew {
				return fmt.Errorf("invalid session-mode %q: must be %q or %q", sessionMode, scheduler.SessionReuse, scheduler.SessionNew)
			}

			id := schedulerShortID()
			now := time.Now().UTC().Format("2006-01-02 15:04:05")

			_, err = q.CreateSchedulerJob(c.Context, sqlc.CreateSchedulerJobParams{
				ID:            id,
				OwnerKind:     scheduler.JobOwnerUser,
				PluginID:      "",
				JobKey:        "",
				RuntimeName:   "",
				Name:          c.String("name"),
				Description:   "",
				ScheduleCron:  cronExpr,
				ScheduleEvery: every,
				ScheduleAt:    at,
				Message:       c.String("message"),
				Payload:       "{}",
				SessionMode:   sessionMode,
				Enabled:       1,
				AgentID:       sql.NullString{},
				UserID:        sql.NullInt64{Int64: userID, Valid: userID != 0},
				CreatedAt:     now,
				UpdatedAt:     now,
				LastRunAt:     sql.NullString{},
				LastError:     "",
			})
			if err != nil {
				return fmt.Errorf("create job: %w", err)
			}

			result := map[string]any{
				"id":           id,
				"name":         c.String("name"),
				"schedule":     sched,
				"session_mode": sessionMode,
				"enabled":      true,
				"created_at":   now,
			}
			out, _ := json.MarshalIndent(result, "", "  ")
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
			q, userID, rawDB, err := openSchedulerDB(c)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := rawDB.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			rows, err := q.ListSchedulerJobs(c.Context)
			if err != nil {
				return fmt.Errorf("list jobs: %w", err)
			}

			type jobOut struct {
				ID          string  `json:"id"`
				Name        string  `json:"name"`
				Cron        string  `json:"cron,omitempty"`
				Every       string  `json:"every,omitempty"`
				At          string  `json:"at,omitempty"`
				Message     string  `json:"message,omitempty"`
				SessionMode string  `json:"session_mode"`
				Enabled     bool    `json:"enabled"`
				LastRunAt   *string `json:"last_run_at,omitempty"`
				LastError   string  `json:"last_error,omitempty"`
				CreatedAt   string  `json:"created_at"`
			}

			visible := make([]jobOut, 0, len(rows))
			for _, row := range rows {
				if row.OwnerKind == scheduler.JobOwnerPlugin {
					continue
				}
				if row.UserID.Valid && row.UserID.Int64 != userID {
					continue
				}
				j := jobOut{
					ID:          row.ID,
					Name:        row.Name,
					Cron:        row.ScheduleCron,
					Every:       row.ScheduleEvery,
					At:          row.ScheduleAt,
					Message:     row.Message,
					SessionMode: row.SessionMode,
					Enabled:     row.Enabled != 0,
					LastError:   row.LastError,
					CreatedAt:   row.CreatedAt,
				}
				if row.LastRunAt.Valid {
					j.LastRunAt = &row.LastRunAt.String
				}
				visible = append(visible, j)
			}

			if len(visible) == 0 {
				fmt.Println("[]")
				return nil
			}

			out, _ := json.MarshalIndent(visible, "", "  ")
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

			q, userID, rawDB, err := openSchedulerDB(c)
			if err != nil {
				return err
			}
			defer func() {
				if cerr := rawDB.Close(); cerr != nil && err == nil {
					err = cerr
				}
			}()

			row, err := q.GetSchedulerJob(c.Context, id)
			if err != nil {
				return fmt.Errorf("job %q not found", id)
			}
			if row.OwnerKind == scheduler.JobOwnerPlugin {
				return fmt.Errorf("job %q is plugin-owned and cannot be removed via CLI", id)
			}
			if row.UserID.Valid && row.UserID.Int64 != userID {
				return fmt.Errorf("job %q not found or access denied", id)
			}

			if err := q.DeleteSchedulerJob(c.Context, id); err != nil {
				return fmt.Errorf("remove job: %w", err)
			}

			fmt.Printf("Job %q removed.\n", id)
			return nil
		},
	}
}

func validateScheduleArgs(sched scheduler.Schedule) error {
	count := 0
	if sched.Cron != "" {
		count++
	}
	if sched.Every != "" {
		count++
	}
	if sched.At != "" {
		count++
	}
	if count == 0 {
		return fmt.Errorf("one of --cron, --every, or --at is required")
	}
	if count > 1 {
		return fmt.Errorf("only one of --cron, --every, or --at may be specified")
	}
	if sched.Every != "" {
		if _, err := time.ParseDuration(sched.Every); err != nil {
			return fmt.Errorf("invalid --every %q: %w", sched.Every, err)
		}
	}
	if sched.At != "" {
		t, err := time.Parse(time.RFC3339, sched.At)
		if err != nil {
			return fmt.Errorf("invalid --at %q: must be RFC3339 format: %w", sched.At, err)
		}
		if !t.After(time.Now()) {
			return fmt.Errorf("--at timestamp %q is in the past", sched.At)
		}
	}
	return nil
}

func schedulerShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
