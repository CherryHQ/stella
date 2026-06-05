package main

import (
	"fmt"
	"net/http"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
	"github.com/CherryHQ/stella/internal/cli"
)

func goalCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "goal",
		Usage:    "Manage high-level goals that roll up from child tasks",
		Category: "Feature",
		Description: `Create and track goals. A goal is a container whose status
rolls up from its child tasks; it is not dispatched directly.`,
		Subcommands: []*ucli.Command{
			goalListCmd(),
			goalGetCmd(),
			goalCreateCmd(),
			goalActivateCmd(),
			goalCancelCmd(),
			goalTasksCmd(),
			goalReviewsCmd(),
			goalReviewCmd(),
		},
	}
}

func goalReviewsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "reviews",
		Usage:     "List goal reviews",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			list, err := apiclient.Call[apitypes.ReviewList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoalReviews(c.Context, id, nil)
			})
			if err != nil {
				return fmt.Errorf("goal reviews: %w", err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			return printReviewList(c, list.Reviews)
		},
	}
}

func goalListCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List goals",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "limit", Usage: "Maximum number of goals to return"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			params := &apiclient.ListGoalsParams{}
			if c.IsSet("limit") {
				l := c.Int("limit")
				params.PageSize = &l
			}
			list, err := apiclient.Call[apitypes.GoalList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoals(c.Context, params)
			})
			if err != nil {
				return fmt.Errorf("list goals: %w", err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			for _, g := range list.Goals {
				o.Printf("%-36s  %-10s  %-8s  %s\n", g.Id, g.Status, g.Priority, g.Title)
			}
			return o.Err()
		},
	}
}

func goalGetCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a goal",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			goal, err := apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetGoal(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("get goal: %w", err)
			}
			return printGoal(c, goal)
		},
	}
}

func goalCreateCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a goal",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Required: true, Usage: "Goal title"},
			&ucli.StringFlag{Name: "description", Usage: "Goal description"},
			&ucli.StringFlag{Name: "project-id", Usage: "Project/workspace context"},
			&ucli.StringFlag{Name: "priority", Value: "routine", Usage: "routine | urgent"},
			&ucli.StringFlag{Name: "review-policy", Usage: "none (only supported value in this build)"},
			&ucli.BoolFlag{Name: "activate", Usage: "Activate (draft -> ready) immediately"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}

			body := apitypes.CreateGoalRequest{Title: c.String("title"), AgentId: agentID}
			if d := c.String("description"); d != "" {
				body.Description = &d
			}
			if p := c.String("project-id"); p != "" {
				body.ProjectId = &p
			}
			if p := c.String("priority"); p != "" {
				prio := apitypes.CreateGoalRequestPriority(p)
				body.Priority = &prio
			}
			if rp := c.String("review-policy"); rp != "" {
				pol := apitypes.CreateGoalRequestReviewPolicy(rp)
				body.ReviewPolicy = &pol
			}
			goal, err := apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateGoal(c.Context, body)
			})
			if err != nil {
				return fmt.Errorf("create goal: %w", err)
			}
			if c.Bool("activate") {
				goal, err = apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
					return api.ActivateGoal(c.Context, goal.Id)
				})
				if err != nil {
					return fmt.Errorf("activate goal: %w", err)
				}
			}
			return printGoal(c, goal)
		},
	}
}

func goalActivateCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "activate",
		Usage:     "Activate a draft goal",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			goal, err := apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.ActivateGoal(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("activate goal: %w", err)
			}
			return printGoal(c, goal)
		},
	}
}

func goalCancelCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "cancel",
		Usage:     "Cancel a goal",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{&ucli.StringFlag{Name: "reason", Usage: "Cancellation reason"}, cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			body := apitypes.CancelGoalRequest{}
			if r := c.String("reason"); r != "" {
				body.Reason = &r
			}
			goal, err := apiclient.Call[apitypes.Goal](func(api *apiclient.Client) (*http.Response, error) {
				return api.CancelGoal(c.Context, id, body)
			})
			if err != nil {
				return fmt.Errorf("cancel goal: %w", err)
			}
			return printGoal(c, goal)
		},
	}
}

func goalTasksCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "tasks",
		Usage:     "List a goal's child tasks",
		ArgsUsage: "<goal-id>",
		Flags:     []ucli.Flag{cli.JSONFlag()},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("goal id is required")
			}
			list, err := apiclient.Call[apitypes.TaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListGoalTasks(c.Context, id, nil)
			})
			if err != nil {
				return fmt.Errorf("goal tasks: %w", err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, list)
			}
			o := cli.Stdout(c)
			for _, t := range list.Tasks {
				o.Printf("%-36s  %-10s  %-8s  %s\n", t.Id, t.Status, t.Priority, t.Title)
			}
			return o.Err()
		},
	}
}

func goalReviewCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "review",
		Usage: "Decide on a goal review",
		Subcommands: []*ucli.Command{
			goalReviewDecisionCmd("approve", "Approve a review"),
			goalReviewDecisionCmd("reject", "Reject a review"),
			goalReviewDecisionCmd("request-changes", "Request changes on a review"),
			goalReviewDecisionCmd("escalate", "Escalate an agent review to a human"),
		},
	}
}

func goalReviewDecisionCmd(verb, usage string) *ucli.Command {
	return &ucli.Command{
		Name:      verb,
		Usage:     usage,
		ArgsUsage: "<goal-id> <review-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "summary", Usage: "Decision summary"},
			&ucli.StringFlag{Name: "feedback", Usage: "Feedback (reject / request-changes)"},
			&ucli.StringFlag{Name: "reason", Usage: "Reason (escalate)"},
			cli.JSONFlag(),
		},
		Action: func(c *ucli.Context) error {
			if c.NArg() < 2 {
				return fmt.Errorf("usage: stella task goal review %s <goal-id> <review-id>", verb)
			}
			goalID := c.Args().Get(0)
			reviewID := c.Args().Get(1)
			rev, err := apiclient.Call[apitypes.Review](func(api *apiclient.Client) (*http.Response, error) {
				switch verb {
				case "approve":
					return api.ApproveGoalReview(c.Context, goalID, reviewID, reviewDecisionBody(c))
				case "reject":
					return api.RejectGoalReview(c.Context, goalID, reviewID, reviewDecisionBody(c))
				case "request-changes":
					return api.RequestChangesGoalReview(c.Context, goalID, reviewID, reviewDecisionBody(c))
				case "escalate":
					body := apitypes.EscalateReviewRequest{}
					if r := c.String("reason"); r != "" {
						body.Reason = &r
					}
					return api.EscalateGoalReview(c.Context, goalID, reviewID, body)
				}
				return nil, fmt.Errorf("unknown verb %q", verb)
			})
			if err != nil {
				return fmt.Errorf("goal review %s: %w", verb, err)
			}
			if cli.IsJSON(c) {
				return cli.PrintJSON(c, rev)
			}
			o := cli.Stdout(c)
			o.Printf("review %s -> %s\n", rev.Id, rev.Status)
			return o.Err()
		},
	}
}

func reviewDecisionBody(c *ucli.Context) apitypes.ReviewDecisionRequest {
	body := apitypes.ReviewDecisionRequest{}
	if s := c.String("summary"); s != "" {
		body.Summary = &s
	}
	if f := c.String("feedback"); f != "" {
		body.Feedback = &f
	}
	return body
}

func printGoal(c *ucli.Context, g apitypes.Goal) error {
	if cli.IsJSON(c) {
		return cli.PrintJSON(c, g)
	}
	o := cli.Stdout(c)
	o.Printf("id:       %s\n", g.Id)
	o.Printf("title:    %s\n", g.Title)
	o.Printf("status:   %s\n", g.Status)
	o.Printf("priority: %s\n", g.Priority)
	o.Printf("review:   %s\n", g.ReviewPolicy)
	o.Printf("created:  %s\n", g.CreatedAt.Format("2006-01-02 15:04:05"))
	return o.Err()
}
