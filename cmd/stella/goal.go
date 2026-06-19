package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

// goal.go is the agent-facing surface for durable, self-driven work. A goal is
// a root goal: async work that survives restarts and converges to an
// acceptance contract. Creating one with --activate (the default) hands the
// goal to the background dispatcher, which claims and runs it without further
// prompting — this is how an agent schedules and pursues work that outlives the
// current conversation. Like every other stella CLI command, it is a thin
// client over the goal HTTP API.

func goalCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "goal",
		Aliases:  []string{"task"},
		Usage:    "Create and track durable goals the dispatcher runs to acceptance in the background",
		Category: "Feature",
		Description: `A goal is durable, async work that survives restarts and is accepted, not
just finished. Create one to hand yourself a long-running objective: the
dispatcher claims it and drives it through a bounded rework loop until its
acceptance contract passes — no further prompting needed.

Use this for work that outlives a single conversation: long research,
multi-step builds, anything you want carried to completion on its own.`,
		Subcommands: []*ucli.Command{
			goalCreateCommand(),
			goalListCommand(),
			goalGetCommand(),
			goalCancelCommand(),
		},
	}
}

func goalCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a goal and, by default, run it immediately in the background",
		Description: `Creates a goal and (unless --activate=false) activates it so the dispatcher
picks it up and executes it in the background. A leaf (default) is executed
directly; a composite must be planned via the Web UI before it runs.

The background worker sees only the title and intent you provide — write a
clear, self-contained intent describing what "done" means.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "Short title (required)", Required: true},
			&ucli.StringFlag{Name: "intent", Usage: `What "done" means — the acceptance target in prose`},
			&ucli.StringFlag{Name: "kind", Usage: "leaf (default, directly executed) or composite (planned first)", Value: "leaf"},
			&ucli.StringFlag{Name: "priority", Usage: "routine (default) or urgent"},
			&ucli.BoolFlag{Name: "activate", Usage: "Activate immediately for a direct background run", Value: true},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			activate := c.Bool("activate")
			body := apiclient.CreateGoalJSONRequestBody{
				Title:    c.String("title"),
				AgentId:  agentID,
				Activate: &activate,
			}
			if v := c.String("intent"); v != "" {
				body.Intent = &v
			}
			if v := c.String("kind"); v != "" {
				k := apitypes.CreateGoalRequestKind(v)
				body.Kind = &k
			}
			if v := c.String("priority"); v != "" {
				p := apitypes.CreateGoalRequestPriority(v)
				body.Priority = &p
			}

			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateGoal(c.Context, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Goal %s created (%s, %s).\n", cli.ShortID(d.Id), d.Title, d.Lifecycle)
			return o.Err()
		},
	}
}

func goalListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List your goals (active ones by default)",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "terminal", Usage: "Show terminal/history goals instead of active ones"},
			&ucli.StringFlag{Name: "q", Usage: "Case-insensitive substring match on title/intent"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			params := &apiclient.ListGoalsParams{AgentId: &agentID}
			if c.IsSet("terminal") {
				params.Terminal = apiclient.Ptr(c.Bool("terminal"))
			}
			if v := c.String("q"); v != "" {
				params.Q = &v
			}

			list, err := apiclient.Call[apiclient.GoalList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoals(c.Context, params)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Goals) == 0 {
				o.Println("No goals.")
				return o.Err()
			}
			o.Printf("%-10s  %-28s  %-10s  %-14s  %s\n", "ID", "TITLE", "KIND", "LIFECYCLE", "ACCEPTANCE")
			for _, d := range list.Goals {
				o.Printf("%-10s  %-28s  %-10s  %-14s  %s\n",
					cli.ShortID(d.Id), cli.Truncate(d.Title, 28), string(d.Kind), string(d.Lifecycle), string(d.AcceptanceState))
			}
			return o.Err()
		},
	}
}

func goalGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a goal's current state",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal get <goal-id>")
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetGoal(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("ID:         %s\n", d.Id)
			o.Printf("Title:      %s\n", d.Title)
			o.Printf("Kind:       %s\n", string(d.Kind))
			o.Printf("Lifecycle:  %s\n", string(d.Lifecycle))
			o.Printf("Acceptance: %s\n", string(d.AcceptanceState))
			if d.Intent != nil && *d.Intent != "" {
				o.Printf("Intent:     %s\n", *d.Intent)
			}
			if d.AttemptCount != nil {
				o.Printf("Attempts:   %d\n", *d.AttemptCount)
			}
			return o.Err()
		},
	}
}

func goalCancelCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "cancel",
		Usage:     "Cancel a goal (cascades to its non-terminal subtree)",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "Optional cancellation reason"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal cancel <goal-id>")
			}
			var body apiclient.CancelGoalJSONRequestBody
			if v := c.String("reason"); v != "" {
				body.Reason = &v
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.CancelGoal(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Goal %s cancelled (%s).\n", cli.ShortID(d.Id), d.Lifecycle)
			return o.Err()
		},
	}
}
