package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

// task_goal_plan.go is the CLI surface for the structured-plan lifecycle (#525):
// stage a plan, accept it (or route it through a human review), then materialize
// it into the goal's task graph.
func goalPlanCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "plan",
		Usage: "Manage a goal's structured plan",
		Description: `A goal's work tasks come only from an accepted, materialized plan.
Stage a plan with 'set', accept it directly ('accept', review_policy=none) or via
human review ('submit-review' + 'review approve'), then 'materialize' it.`,
		Subcommands: []*ucli.Command{
			goalPlanGetCmd(),
			goalPlanStartCmd(),
			goalPlanSetCmd(),
			goalPlanAcceptCmd(),
			goalPlanSubmitReviewCmd(),
			goalPlanMaterializeCmd(),
			goalPlanReviewCmd(),
		},
	}
}

func goalPlanGetCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a goal's plan",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			plan, err := apiclient.Call[apitypes.GoalPlan](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetGoalPlan(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("get plan: %w", err)
			}
			return printPlan(c, plan)
		},
	}
}

func goalPlanStartCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "start",
		Usage:     "Open the goal's dedicated planning session",
		ArgsUsage: "<goal-id>",
		Description: `Opens (creating on first call, reusing thereafter) the session a goal is
planned in, and prints its id. Delegate planning into this session and author
the plan there with 'stella task goal plan set'; the user re-opens the same
session from the web UI to refine the plan by chatting.`,
		Flags: []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			sess, err := apiclient.Call[apitypes.GoalPlanningSession](func(api *apiclient.Client) (*http.Response, error) {
				return api.StartGoalPlanning(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("start planning: %w", err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, sess)
			}
			o := cli.Stdout(c)
			o.Printf("planning session: %s\n", sess.SessionId)
			return o.Err()
		},
	}
}

func goalPlanSetCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "set",
		Usage:     "Create or replace a goal's pending plan edit",
		ArgsUsage: "<goal-id>",
		Description: `Reads a PlanContent JSON document ({"items":[...]}) from --file
(use '-' for stdin) and stages it as the goal's pending plan edit.`,
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "file", Required: true, Usage: "Path to PlanContent JSON, or '-' for stdin"},
			&ucli.StringFlag{Name: "review-policy", Usage: "none (default) | human"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			raw, err := readFileOrStdin(c.String("file"))
			if err != nil {
				return fmt.Errorf("read plan: %w", err)
			}
			var content apitypes.PlanContent
			if err := json.Unmarshal(raw, &content); err != nil {
				return fmt.Errorf("parse plan JSON: %w", err)
			}
			body := apitypes.PutGoalPlanRequest{Content: content}
			if rp := c.String("review-policy"); rp != "" {
				pol := apitypes.PutGoalPlanRequestReviewPolicy(rp)
				body.ReviewPolicy = &pol
			}
			plan, err := apiclient.Call[apitypes.GoalPlan](func(api *apiclient.Client) (*http.Response, error) {
				return api.PutGoalPlan(c.Context, id, body)
			})
			if err != nil {
				return fmt.Errorf("set plan: %w", err)
			}
			return printPlan(c, plan)
		},
	}
}

func goalPlanAcceptCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "accept",
		Usage:     "Accept a goal's pending plan (review_policy=none)",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			plan, err := apiclient.Call[apitypes.GoalPlan](func(api *apiclient.Client) (*http.Response, error) {
				return api.AcceptGoalPlan(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("accept plan: %w", err)
			}
			return printPlan(c, plan)
		},
	}
}

func goalPlanSubmitReviewCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "submit-review",
		Usage:     "Submit a goal's pending plan edit for human review",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			rev, err := apiclient.Call[apitypes.Review](func(api *apiclient.Client) (*http.Response, error) {
				return api.SubmitGoalPlanReview(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("submit plan review: %w", err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, rev)
			}
			o := cli.Stdout(c)
			o.Printf("plan review %s -> %s\n", rev.Id, rev.Status)
			return o.Err()
		},
	}
}

func goalPlanMaterializeCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "materialize",
		Usage:     "Materialize an accepted/approved plan into the goal's task graph",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			goal, err := apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.MaterializeGoalPlan(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("materialize plan: %w", err)
			}
			return printGoal(c, goal)
		},
	}
}

func goalPlanReviewCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "review",
		Usage: "Decide on a goal plan review",
		Subcommands: []*ucli.Command{
			goalPlanReviewDecisionCmd("approve", "Approve a plan review"),
			goalPlanReviewDecisionCmd("reject", "Reject a plan review (discards the pending edit)"),
			goalPlanReviewDecisionCmd("request-changes", "Request changes (keeps the pending edit)"),
		},
	}
}

func goalPlanReviewDecisionCmd(verb, usage string) *ucli.Command {
	return &ucli.Command{
		Name:      verb,
		Usage:     usage,
		ArgsUsage: "<goal-id> <review-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "summary", Usage: "Decision summary"},
			&ucli.StringFlag{Name: "feedback", Usage: "Feedback (reject / request-changes)"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			if c.NArg() < 2 {
				return fmt.Errorf("usage: stella task goal plan review %s <goal-id> <review-id>", verb)
			}
			goalID := c.Args().Get(0)
			reviewID := c.Args().Get(1)
			rev, err := apiclient.Call[apitypes.Review](func(api *apiclient.Client) (*http.Response, error) {
				switch verb {
				case "approve":
					return api.ApproveGoalPlanReview(c.Context, goalID, reviewID, reviewDecisionBody(c))
				case "reject":
					return api.RejectGoalPlanReview(c.Context, goalID, reviewID, reviewDecisionBody(c))
				case "request-changes":
					return api.RequestChangesGoalPlanReview(c.Context, goalID, reviewID, reviewDecisionBody(c))
				}
				return nil, fmt.Errorf("unknown verb %q", verb)
			})
			if err != nil {
				return fmt.Errorf("goal plan review %s: %w", verb, err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, rev)
			}
			o := cli.Stdout(c)
			o.Printf("plan review %s -> %s\n", rev.Id, rev.Status)
			return o.Err()
		},
	}
}

func printPlan(c *ucli.Context, p apitypes.GoalPlan) error {
	if cli.IsJSON(c) {
		return cli.PrintJSON(c, p)
	}
	o := cli.Stdout(c)
	o.Printf("id:           %s\n", p.Id)
	o.Printf("goal:         %s\n", p.GoalId)
	o.Printf("status:       %s\n", p.Status)
	o.Printf("review:       %s\n", p.ReviewPolicy)
	if p.PlanningSessionId != nil && *p.PlanningSessionId != "" {
		o.Printf("planning sess: %s\n", *p.PlanningSessionId)
	}
	o.Printf("items:        %d\n", len(p.Content.Items))
	if p.PendingContent != nil {
		o.Printf("pending:      %d items\n", len(p.PendingContent.Items))
	}
	if p.MaterializedAt != nil {
		o.Printf("materialized: %s\n", p.MaterializedAt.Format("2006-01-02 15:04:05"))
	}
	return o.Err()
}

// readFileOrStdin reads the named file, or stdin when path is "-".
func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
