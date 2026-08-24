package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"time"

	ucli "github.com/urfave/cli/v2"

	"github.com/CherryHQ/stella/internal/runtimeops"
)

// The runtime domain is the operator escape hatch for the two distributed
// fencing states that deliberately never resolve on their own: a poison FIFO
// head that exhausted automatic retry, and a fenced SessionSandbox generation
// whose resource cleanup cannot prove absence. Both fail closed by design;
// these commands are the attributed human transition out. All behavior lives
// in internal/runtimeops; this file only wires and renders.
func runtimeCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "runtime",
		Usage:    "Inspect and resolve distributed runtime fencing state",
		Category: "Admin",
		Subcommands: []*ucli.Command{
			runtimeFifoCommand(),
			runtimeSandboxCommand(),
		},
	}
}

func runtimeFifoCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "fifo",
		Usage: "Manage durable channel input items",
		Subcommands: []*ucli.Command{
			{
				Name:  "list",
				Usage: "List channel input items still occupying admission budget",
				Flags: []ucli.Flag{
					&ucli.BoolFlag{Name: "json", Usage: "Emit the result as JSON"},
				},
				Action: func(c *ucli.Context) error {
					return runtimeops.Open(c.Context, func(ctx context.Context, store *runtimeops.Store) error {
						items, err := store.ListFifo(ctx)
						if err != nil {
							return fmt.Errorf("runtime fifo list: %w", err)
						}
						if c.Bool("json") {
							return writeJSON(os.Stdout, items)
						}
						writeFifoTable(os.Stdout, items)
						return nil
					})
				},
			},
			{
				Name:      "reject",
				Usage:     "Reject a channel input item so the binding's queue can move past it",
				ArgsUsage: "<fifo-id>",
				Description: "Rejection is terminal and attributed: the item's content is discarded, " +
					"the message will never run, and the rejecting operator is recorded on the row. " +
					"Use it on a poison head that exhausted automatic retry (see `runtime fifo list`).",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "reason", Usage: "Why the item is being rejected", Required: true},
					&ucli.StringFlag{Name: "by", Usage: "Operator name recorded on the rejection (default: OS user)"},
				},
				Action: func(c *ucli.Context) error {
					id := c.Args().First()
					if id == "" || c.NArg() != 1 {
						return errors.New("runtime fifo reject: exactly one <fifo-id> argument is required")
					}
					by := c.String("by")
					if by == "" {
						by = osUserName()
					}
					return runtimeops.Open(c.Context, func(ctx context.Context, store *runtimeops.Store) error {
						rejected, err := store.RejectFifo(ctx, id, c.String("reason"), by)
						if err != nil {
							return fmt.Errorf("runtime fifo reject: %w", err)
						}
						if !rejected {
							return fmt.Errorf("runtime fifo reject: item %s not found or already terminal", id)
						}
						fprintf(os.Stdout, "Rejected %s (by %s)\n", id, by)
						return nil
					})
				},
			},
		},
	}
}

func runtimeSandboxCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "sandbox",
		Usage: "Manage SessionSandbox generations",
		Subcommands: []*ucli.Command{
			{
				Name:  "list",
				Usage: "List sandbox generations that still hold (or may hold) a resource",
				Flags: []ucli.Flag{
					&ucli.BoolFlag{Name: "json", Usage: "Emit the result as JSON"},
				},
				Action: func(c *ucli.Context) error {
					return runtimeops.Open(c.Context, func(ctx context.Context, store *runtimeops.Store) error {
						generations, err := store.ListSandbox(ctx)
						if err != nil {
							return fmt.Errorf("runtime sandbox list: %w", err)
						}
						if c.Bool("json") {
							return writeJSON(os.Stdout, generations)
						}
						writeSandboxTable(os.Stdout, generations)
						return nil
					})
				},
			},
			{
				Name:      "mark-destroyed",
				Usage:     "Record a fenced sandbox generation as destroyed after manual cleanup",
				ArgsUsage: "<session-id>",
				Description: "A generation stays 'fenced' when cleanup could not prove its resource " +
					"(a process tree or container) is gone, and the session cannot create a new sandbox " +
					"until it is resolved. This command substitutes the operator's verification for that " +
					"proof: confirm yourself that the resource shown no longer exists, then re-run with " +
					"--force. Marking a generation destroyed while its resource still runs allows a new " +
					"sandbox beside the old one.",
				Flags: []ucli.Flag{
					&ucli.BoolFlag{Name: "force", Usage: "Perform the transition instead of showing what it would affect"},
				},
				Action: func(c *ucli.Context) error {
					sessionID := c.Args().First()
					if sessionID == "" || c.NArg() != 1 {
						return errors.New("runtime sandbox mark-destroyed: exactly one <session-id> argument is required")
					}
					return runtimeops.Open(c.Context, func(ctx context.Context, store *runtimeops.Store) error {
						gen, err := store.GetFencedSandbox(ctx, sessionID)
						if err != nil {
							return fmt.Errorf("runtime sandbox mark-destroyed: %w", err)
						}
						if !c.Bool("force") {
							fprintf(os.Stdout, "Would mark destroyed: session %s generation %d\n  backend:  %s\n  resource: %s\n  fenced:   %s\n\nVerify that this resource no longer exists, then re-run with --force.\n",
								gen.SessionID, gen.Generation, gen.ResourceBackend, gen.ResourceID, formatOptionalTime(gen.FencedAt))
							return nil
						}
						done, err := store.MarkSandboxDestroyed(ctx, sessionID, gen.Generation)
						if err != nil {
							return fmt.Errorf("runtime sandbox mark-destroyed: %w", err)
						}
						if !done {
							return fmt.Errorf("runtime sandbox mark-destroyed: generation %d of session %s changed state concurrently; re-run to see its current state", gen.Generation, sessionID)
						}
						fprintf(os.Stdout, "Marked destroyed: session %s generation %d\n", sessionID, gen.Generation)
						return nil
					})
				},
			},
		},
	}
}

func osUserName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "operator"
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeFifoTable(out io.Writer, items []runtimeops.FifoItem) {
	if len(items) == 0 {
		fprintf(out, "No live channel input items.\n")
		return
	}
	fprintf(out, "%-38s %-8s %-8s %-9s %-24s %s\n", "ID", "STATUS", "KIND", "ATTEMPTS", "BINDING", "NOTE")
	for _, item := range items {
		note := item.BlockedReason
		if item.RetryExhausted {
			note = "retry exhausted; needs `runtime fifo reject` — " + note
		}
		fprintf(out, "%-38s %-8s %-8s %-9d %-24s %s\n", item.ID, item.Status, item.Kind, item.AttemptCount, item.BindingKey, note)
	}
}

func writeSandboxTable(out io.Writer, generations []runtimeops.SandboxGeneration) {
	if len(generations) == 0 {
		fprintf(out, "No live sandbox generations.\n")
		return
	}
	fprintf(out, "%-38s %-4s %-9s %-8s %-38s %s\n", "SESSION", "GEN", "STATE", "BACKEND", "RESOURCE", "FENCED")
	for _, g := range generations {
		fprintf(out, "%-38s %-4d %-9s %-8s %-38s %s\n", g.SessionID, g.Generation, g.State, g.ResourceBackend, g.ResourceID, formatOptionalTime(g.FencedAt))
	}
}

func formatOptionalTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
