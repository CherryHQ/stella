package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
	"github.com/CherryHQ/stella/pkg/renderrefs"
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
			goalHealthCommand(),
			goalCancelCommand(),
			goalApproveCommand(),
			goalRejectCommand(),
			goalChildrenCommand(),
			goalActivateCommand(),
		},
	}
}

func goalCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a goal; it is planned and run in the background automatically",
		Description: `Creates a goal and runs it in the background to acceptance. Every goal is
planned first: the dispatcher autonomously decomposes it into verifiable
sub-tasks, runs them, and converges the goal until its acceptance contract
passes — you never pick "leaf vs composite" or call plan/approve/activate by
hand.

The planner and workers see only the title and intent you provide — write a
clear, self-contained intent describing what "done" means.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "Short title (required)", Required: true},
			&ucli.StringFlag{Name: "intent", Usage: `What "done" means — the acceptance target in prose`},
			&ucli.StringFlag{Name: "priority", Usage: "routine (default) or urgent"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := scopedAgentIDFromEnv()
			if err != nil {
				return err
			}
			body := apiclient.CreateGoalJSONRequestBody{
				Title:   c.String("title"),
				AgentId: agentID,
			}
			if v := c.String("intent"); v != "" {
				body.Intent = &v
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
			// Best-effort: a failed sentinel write must never fail the create.
			_ = renderrefs.Emit(c.App.ErrWriter, renderrefs.Reference{
				Type:    "goal",
				ID:      d.Id,
				Intent:  "created",
				AgentID: agentID,
				Preview: &renderrefs.Preview{Title: d.Title, Status: string(d.Lifecycle)},
			})
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

// goalApproveCommand approves a composite's pending decomposition plan, which
// materializes its children and unblocks it. Only a composite blocked on
// needs_plan_approval (review_policy=human) has a plan awaiting a decision;
// review_policy=none composites materialize automatically with no gate.
func goalApproveCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "approve",
		Usage:     "Approve a composite's pending plan and materialize its children",
		ArgsUsage: "<goal-id>",
		Description: `Approves the decomposition plan a composite is blocked on (needs_plan_approval).
Inspect the pending plan first with "stella goal get <goal-id>" (the plan field).
Approving materializes the children and unblocks the composite so the dispatcher
runs them.`,
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal approve <goal-id>")
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.ApprovePlan(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Plan for goal %s approved and materialized (%s).\n", cli.ShortID(d.Id), d.Lifecycle)
			o.Printf("See children: stella goal children %s\n", cli.ShortID(id))
			return o.Err()
		},
	}
}

// goalRejectCommand rejects a composite's pending plan, sending it back to draft
// so the dispatcher re-decomposes it.
func goalRejectCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "reject",
		Usage:     "Reject a composite's pending plan (it returns to draft for re-decomposition)",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "reason", Usage: "Optional rejection reason"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal reject <goal-id>")
			}
			var body apiclient.RejectPlanJSONRequestBody
			if v := c.String("reason"); v != "" {
				body.Reason = &v
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.RejectPlan(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Plan for goal %s rejected (%s).\n", cli.ShortID(d.Id), d.Lifecycle)
			return o.Err()
		},
	}
}

func goalChildrenCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "children",
		Usage:     "List a composite's child goals",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal children <goal-id>")
			}
			list, err := apiclient.Call[apiclient.GoalList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoalChildren(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			if len(list.Goals) == 0 {
				o.Println("No children.")
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

// goalActivateCommand hands a draft goal to the dispatcher. For a composite it
// also flips its materialized children draft→ready, so this is the step that
// starts a planned composite running after "approve".
func goalActivateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "activate",
		Usage:     "Activate a draft goal so the dispatcher runs it (composite: also its children)",
		ArgsUsage: "<goal-id>",
		Description: `Moves a draft goal to ready so the background dispatcher claims it. A composite
must be planned first (its plan gate blocks activation until its plan is
materialized into children); activating a composite cascades to its draft
children so the dispatcher can begin claiming the leaves under it.`,
		Flags: []ucli.Flag{
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella goal activate <goal-id>")
			}
			d, err := apiclient.Call[apiclient.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.ActivateGoal(c.Context, id)
			})
			if err != nil {
				return err
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, d)
			}
			o := cli.Stdout(c)
			o.Printf("Goal %s activated (%s).\n", cli.ShortID(d.Id), d.Lifecycle)
			return o.Err()
		},
	}
}
