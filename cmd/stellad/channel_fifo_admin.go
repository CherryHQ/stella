package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/channel"
)

// channelCommand is the operator domain for channel maintenance commands.
func channelCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "channel",
		Usage:    "Maintain channel runtime state",
		Category: "Admin",
		Subcommands: []*ucli.Command{
			channelFIFOCommand(),
		},
	}
}

// channelFIFOCommand is the durable channel FIFO maintenance domain.
func channelFIFOCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "fifo",
		Usage:    "Inspect and recover durable channel FIFO items",
		Category: "Admin",
		Description: "These commands operate directly on the deployment database. " +
			"With embedded PostgreSQL, stop stellad first; no worker or migration is started.",
		Subcommands: []*ucli.Command{
			channelFIFOListCommand(),
			channelFIFOInspectCommand(),
			channelFIFORejectCommand(),
		},
	}
}

func channelFIFOListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List blocked durable channel FIFO item summaries",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "limit",
				Value: channel.DefaultFIFOAdminListLimit,
				Usage: "Maximum number of blocked items to return (1-100)",
			},
			&ucli.BoolFlag{Name: "json", Usage: "Emit summaries as JSON"},
		},
		Action: func(c *ucli.Context) error {
			if c.Args().Len() != 0 {
				return errors.New("fifo list does not accept positional arguments")
			}
			limit := c.Int("limit")
			if err := channel.ValidateFIFOAdminListLimit(limit); err != nil {
				return err
			}
			return withFIFOAdmin(c, func(ctx context.Context, admin *channel.FIFOAdmin) error {
				summaries, err := admin.ListBlocked(ctx, limit)
				if err != nil {
					return fmt.Errorf("fifo list: %w", err)
				}
				return writeFIFOAdminSummaries(fifoAdminWriter(c), summaries, c.Bool("json"))
			})
		},
	}
}

func channelFIFOInspectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "inspect",
		Usage:     "Inspect one exact durable channel FIFO item",
		ArgsUsage: "<fifo-item-id>",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "Emit the item, binding, run, and audit as JSON"},
		},
		Action: func(c *ucli.Context) error {
			if err := validateFIFOAdminTarget(c); err != nil {
				return err
			}
			return withFIFOAdmin(c, func(ctx context.Context, admin *channel.FIFOAdmin) error {
				record, err := admin.Inspect(ctx, c.Args().First())
				if err != nil {
					return fmt.Errorf("fifo inspect: %w", err)
				}
				return writeFIFOAdminRecord(fifoAdminWriter(c), record, c.Bool("json"))
			})
		},
	}
}

func channelFIFORejectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "reject",
		Usage:     "Reject one blocked durable channel FIFO item",
		ArgsUsage: "<fifo-item-id>",
		Description: "Reject is an attributed terminal action. It requires the exact item ID, " +
			"a reason, and --force. The database refuses pending/running items and items with a live AgentRun.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Required: true, Usage: "Audited explanation for rejecting the item"},
			&ucli.StringFlag{Name: "by", Usage: "Operator identity recorded in the audit (default: current OS user)"},
			&ucli.BoolFlag{Name: "force", Usage: "Confirm this exact terminal rejection"},
			&ucli.BoolFlag{Name: "json", Usage: "Emit the result as JSON"},
		},
		Action: func(c *ucli.Context) error {
			if err := validateFIFOAdminTarget(c); err != nil {
				return err
			}
			if strings.TrimSpace(c.String("reason")) == "" {
				return errors.New("fifo reject requires a non-empty --reason")
			}
			if !c.Bool("force") {
				return errors.New("fifo reject requires explicit --force")
			}
			operator := strings.TrimSpace(c.String("by"))
			if operator == "" {
				operator = fifoAdminOperatorName()
			}
			return withFIFOAdmin(c, func(ctx context.Context, admin *channel.FIFOAdmin) error {
				result, err := admin.Reject(ctx, c.Args().First(), operator, c.String("reason"), true)
				if err != nil {
					return fmt.Errorf("fifo reject: %w", err)
				}
				if c.Bool("json") {
					return writeFIFOAdminJSON(fifoAdminWriter(c), result)
				}
				if !result.Changed {
					_, err = fmt.Fprintf(fifoAdminWriter(c), "FIFO item %s was already terminal\n", result.Record.Item.ID)
					return err
				}
				_, err = fmt.Fprintf(fifoAdminWriter(c), "Rejected FIFO item %s (by %s)\n", result.Record.Item.ID, operator)
				return err
			})
		},
	}
}

func validateFIFOAdminTarget(c *ucli.Context) error {
	if c.Args().Len() != 1 || strings.TrimSpace(c.Args().First()) == "" {
		return errors.New("provide exactly one FIFO item ID")
	}
	return nil
}

// withFIFOAdmin opens an existing database without applying migrations or
// starting the server. The shared maintenance opener enforces the same
// embedded/external database policy as sandbox recovery.
func withFIFOAdmin(c *ucli.Context, action func(context.Context, *channel.FIFOAdmin) error) error {
	return withMaintenanceDatabase(c, "stellad-fifo-maintenance", func(ctx context.Context, pool *pgxpool.Pool) error {
		return action(ctx, channel.NewFIFOAdmin(pool))
	})
}

func fifoAdminWriter(c *ucli.Context) io.Writer {
	if c != nil && c.App != nil && c.App.Writer != nil {
		return c.App.Writer
	}
	return os.Stdout
}

func writeFIFOAdminRecord(out io.Writer, record channel.FIFOAdminRecord, asJSON bool) error {
	if asJSON {
		return writeFIFOAdminJSON(out, record)
	}
	run := "none"
	if record.Run != nil {
		run = record.Run.ID + " (" + record.Run.Status + ")"
	}
	_, err := fmt.Fprintf(out,
		"ID: %s\nState: %s\nBinding: %s\nSequence: %d\nPrincipal: %s\nSource: %s\nCommand: %s\nAttempt: %d\nAgentRun: %s\nError: %s\nRejected by: %s\nRejected reason: %s\nCreated: %s\nUpdated: %s\n",
		record.Item.ID, record.Item.State, record.Binding.ID, record.Item.Seq,
		record.Item.PrincipalKey, record.Item.SourceKey, record.Item.Command, record.Item.Attempt,
		run, record.Item.ErrorDetail, record.Item.RejectedBy, record.Item.RejectedReason,
		record.Item.CreatedAt.UTC().Format(time.RFC3339), record.Item.UpdatedAt.UTC().Format(time.RFC3339),
	)
	return err
}

func writeFIFOAdminJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeFIFOAdminSummaries(out io.Writer, summaries []channel.FIFOAdminSummary, asJSON bool) error {
	if asJSON {
		return writeFIFOAdminJSON(out, summaries)
	}
	if _, err := fmt.Fprintln(out, "ID\tBINDING\tSEQ\tATTEMPT\tRUN\tERROR"); err != nil {
		return err
	}
	for _, summary := range summaries {
		run := summary.RunID
		if run == "" {
			run = "-"
		}
		errorSummary := summary.ErrorCode
		if detail := strings.Join(strings.Fields(summary.ErrorDetail), " "); detail != "" {
			if errorSummary != "" {
				errorSummary += ": "
			}
			errorSummary += detail
		}
		if errorSummary == "" {
			errorSummary = "-"
		}
		if len(errorSummary) > 160 {
			errorSummary = errorSummary[:160] + "..."
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\t%d\t%d\t%s\t%s\n",
			summary.ID, summary.BindingID, summary.Seq, summary.Attempt, run, errorSummary); err != nil {
			return err
		}
	}
	return nil
}

func fifoAdminOperatorName() string {
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		return current.Username
	}
	return "operator"
}
