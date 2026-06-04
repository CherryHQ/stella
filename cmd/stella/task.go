package main

import (
	"fmt"
	"net/http"
	"strings"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

func taskAgentID(_ *ucli.Context) (string, error) {
	claims, err := scopedTokenClaimsFromEnv()
	if err != nil {
		return "", err
	}
	if claims.AgentID == "" {
		return "", fmt.Errorf("agent ID is required in STELLA_TOKEN")
	}
	return claims.AgentID, nil
}

func taskAgentFlags() []ucli.Flag {
	return nil
}

func requireTaskAgent(c *ucli.Context) error {
	_, err := taskAgentID(c)
	return err
}

func taskCommand() *ucli.Command {
	return &ucli.Command{
		Name:        "task",
		Usage:       "Manage durable background tasks",
		Category:    "Feature",
		Description: `Create, track, and act on durable background tasks.`,
		Subcommands: []*ucli.Command{
			taskListCmd(),
			taskGetCmd(),
			taskCreateCmd(),
			taskCancelCmd(),
			taskReopenCmd(),
			taskReadinessCmd(),
			taskEventsCmd(),
			taskDepsCmd(),
			taskDepCmd(),
			taskRunsCmd(),
			taskReviewsCmd(),
			taskReviewCmd(),
			taskBlockerCmd(),
			goalCommand(),
		},
	}
}

func taskListCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List tasks",
		Flags: append(taskAgentFlags(),
			&ucli.StringFlag{Name: "status", Usage: "Filter by status"},
			&ucli.StringFlag{Name: "project-id", Usage: "Filter by project/workspace context"},
			jsonFlag(),
		),
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			params := &apiclient.ListTasksParams{AgentId: &agentID}
			if s := c.String("status"); s != "" {
				params.Status = &s
			}
			if p := c.String("project-id"); p != "" {
				params.ProjectId = &p
			}
			list, err := apiclient.Call[apitypes.TaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTasks(c.Context, params)
			})
			if err != nil {
				return fmt.Errorf("list tasks: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, list)
			}
			o := stdout(c)
			for _, t := range list.Tasks {
				o.printf("%-36s  %-10s  %-8s  %s\n", t.Id, t.Status, t.Priority, t.Title)
			}
			return o.Err()
		},
	}
}

func taskGetCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a task",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			task, err := apiclient.Call[apitypes.Task](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetTask(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("get task: %w", err)
			}
			return printTask(c, task)
		},
	}
}

func taskCreateCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a task",
		Flags: append(taskAgentFlags(),
			&ucli.StringFlag{Name: "title", Required: true, Usage: "Task title"},
			&ucli.StringFlag{Name: "description", Usage: "Task description"},
			&ucli.StringFlag{Name: "goal-id", Usage: "Parent goal ID"},
			&ucli.StringFlag{Name: "project-id", Usage: "Project/workspace context"},
			&ucli.StringFlag{Name: "executor", Usage: "Explicit executor agent ID"},
			&ucli.StringFlag{Name: "priority", Value: "routine", Usage: "routine | urgent"},
			&ucli.StringSliceFlag{Name: "dep", Usage: "Dependency: <task-id>[:kind[:on_failure]]; may be repeated"},
			&ucli.BoolFlag{Name: "activate", Usage: "Activate (draft -> ready) immediately"},
			jsonFlag(),
		),
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}

			title := c.String("title")
			body := apitypes.CreateTaskRequest{Title: title, AgentId: &agentID}
			if d := c.String("description"); d != "" {
				body.Description = &d
			}
			if g := c.String("goal-id"); g != "" {
				body.GoalId = &g
			}
			if p := c.String("project-id"); p != "" {
				body.ProjectId = &p
			}
			if e := c.String("executor"); e != "" {
				body.ExecutorAgentId = &e
			}
			if p := c.String("priority"); p != "" {
				prio := apitypes.CreateTaskRequestPriority(p)
				body.Priority = &prio
			}
			if c.Bool("activate") {
				on := true
				body.ActivateOnCreate = &on
			}
			if raw := c.StringSlice("dep"); len(raw) > 0 {
				deps := make([]apitypes.DepInput, 0, len(raw))
				for _, spec := range raw {
					d, err := parseDepSpec(spec)
					if err != nil {
						return err
					}
					deps = append(deps, d)
				}
				body.Deps = &deps
			}
			task, err := apiclient.Call[apitypes.Task](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateTask(c.Context, body)
			})
			if err != nil {
				return fmt.Errorf("create task: %w", err)
			}
			return printTask(c, task)
		},
	}
}

func taskCancelCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "cancel",
		Usage:     "Cancel a task",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), &ucli.StringFlag{Name: "reason", Usage: "Cancellation reason"}, jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			body := apitypes.CancelRequest{}
			if r := c.String("reason"); r != "" {
				body.Reason = &r
			}
			task, err := apiclient.Call[apitypes.Task](func(api *apiclient.Client) (*http.Response, error) {
				return api.CancelTask(c.Context, id, body)
			})
			if err != nil {
				return fmt.Errorf("cancel task: %w", err)
			}
			return printTask(c, task)
		},
	}
}

func taskReopenCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "reopen",
		Usage:     "Reopen a done/failed task",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), &ucli.BoolFlag{Name: "cascade", Usage: "Cascade-reset downstream tasks"}, jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			body := apitypes.ReopenRequest{}
			if c.Bool("cascade") {
				v := true
				body.Cascade = &v
			}
			task, err := apiclient.Call[apitypes.Task](func(api *apiclient.Client) (*http.Response, error) {
				return api.ReopenTask(c.Context, id, body)
			})
			if err != nil {
				return fmt.Errorf("reopen task: %w", err)
			}
			return printTask(c, task)
		},
	}
}

func taskReadinessCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "readiness",
		Usage:     "Show task readiness",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			rd, err := apiclient.Call[apitypes.Readiness](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetTaskReadiness(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("readiness: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, rd)
			}
			o := stdout(c)
			o.printf("state:        %s\n", rd.State)
			o.printf("dispatchable: %t\n", rd.Dispatchable)
			if rd.Reasons != nil {
				for _, r := range *rd.Reasons {
					o.printf("  - %s", r.Type)
					if r.UpstreamId != nil {
						o.printf(" upstream=%s", *r.UpstreamId)
					}
					if r.Detail != nil {
						o.printf(" detail=%s", *r.Detail)
					}
					o.println()
				}
			}
			return o.Err()
		},
	}
}

func taskEventsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "events",
		Usage:     "List task events",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.EventList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTaskEvents(c.Context, id, nil)
			})
			if err != nil {
				return fmt.Errorf("events: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, list)
			}
			o := stdout(c)
			for _, e := range list.Events {
				from := ""
				if e.FromStatus != nil {
					from = *e.FromStatus
				}
				to := ""
				if e.ToStatus != nil {
					to = *e.ToStatus
				}
				o.printf("%-19s  %-18s  %s -> %s\n",
					e.CreatedAt.Format("2006-01-02 15:04:05"), e.EventType, from, to)
			}
			return o.Err()
		},
	}
}

func taskDepsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "deps",
		Usage:     "List dependency edges",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.DepList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTaskDeps(c.Context, id, nil)
			})
			if err != nil {
				return fmt.Errorf("deps: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, list)
			}
			o := stdout(c)
			for _, d := range list.Deps {
				up := ""
				if d.UpstreamStatus != nil {
					up = *d.UpstreamStatus
				}
				o.printf("%-36s  %-6s  %-7s  upstream=%s\n", d.DepTaskId, d.DepKind, d.OnFailure, up)
			}
			return o.Err()
		},
	}
}

func taskReviewCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "review",
		Usage: "Decide on a task review",
		Subcommands: []*ucli.Command{
			reviewDecisionCmd("approve", "Approve a review"),
			reviewDecisionCmd("reject", "Reject a review"),
			reviewDecisionCmd("request-changes", "Request changes on a review"),
			taskReviewEscalateCmd(),
		},
	}
}

func taskReviewEscalateCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "escalate",
		Usage:     "Escalate an agent review to a human",
		ArgsUsage: "<task-id> <review-id>",
		Flags:     append(taskAgentFlags(), &ucli.StringFlag{Name: "reason", Usage: "Escalation reason"}, jsonFlag()),
		Action: func(c *ucli.Context) error {
			if c.NArg() < 2 {
				return fmt.Errorf("usage: stella task review escalate <task-id> <review-id>")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			taskID := c.Args().Get(0)
			reviewID := c.Args().Get(1)
			body := apitypes.EscalateReviewRequest{}
			if r := c.String("reason"); r != "" {
				body.Reason = &r
			}
			rev, err := apiclient.Call[apitypes.Review](func(api *apiclient.Client) (*http.Response, error) {
				return api.EscalateTaskReview(c.Context, taskID, reviewID, body)
			})
			if err != nil {
				return fmt.Errorf("review escalate: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, rev)
			}
			o := stdout(c)
			o.printf("review %s -> %s\n", rev.Id, rev.Status)
			return o.Err()
		},
	}
}

func taskReviewsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "reviews",
		Usage:     "List task reviews",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.ReviewList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTaskReviews(c.Context, id, nil)
			})
			if err != nil {
				return fmt.Errorf("reviews: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, list)
			}
			return printReviewList(c, list.Reviews)
		},
	}
}

func taskRunsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "runs",
		Usage:     "List task run attempts",
		ArgsUsage: "<task-id>",
		Flags:     append(taskAgentFlags(), jsonFlag()),
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.RunList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTaskRuns(c.Context, id, nil)
			})
			if err != nil {
				return fmt.Errorf("runs: %w", err)
			}
			if isJSON(c) {
				return printJSON(c, list)
			}
			o := stdout(c)
			for _, r := range list.Runs {
				errStr := ""
				if r.Error != nil {
					errStr = *r.Error
				}
				o.printf("%-36s  %-9s  %-11s  #%d  %s\n", r.Id, r.Kind, r.Status, r.AttemptNo, errStr)
			}
			return o.Err()
		},
	}
}

func taskDepCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "dep",
		Usage: "Add or waive a dependency edge",
		Subcommands: []*ucli.Command{
			{
				Name:      "add",
				Usage:     "Add a dependency edge",
				ArgsUsage: "<task-id> <dep-spec>",
				Description: "dep-spec is <dep-task-id>[:kind[:on_failure]] " +
					"(kind: hard|soft, on_failure: block|fail|ignore).",
				Flags: append(taskAgentFlags(), jsonFlag()),
				Action: func(c *ucli.Context) error {
					if c.NArg() < 2 {
						return fmt.Errorf("usage: stella task dep add <task-id> <dep-spec>")
					}
					if err := requireTaskAgent(c); err != nil {
						return err
					}
					taskID := c.Args().Get(0)
					spec, err := parseDepSpec(c.Args().Get(1))
					if err != nil {
						return err
					}
					body := apitypes.AddDepRequest{DepTaskId: spec.DepTaskId}
					if spec.Kind != nil {
						k := apitypes.AddDepRequestKind(*spec.Kind)
						body.Kind = &k
					}
					if spec.OnFailure != nil {
						f := apitypes.AddDepRequestOnFailure(*spec.OnFailure)
						body.OnFailure = &f
					}
					if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
						return api.AddTaskDep(c.Context, taskID, body)
					}); err != nil {
						return fmt.Errorf("add dep: %w", err)
					}
					if isJSON(c) {
						return printJSON(c, depResult{TaskID: taskID, DepTaskID: spec.DepTaskId, Action: "added"})
					}
					o := stdout(c)
					o.printf("added dep %s -> %s\n", taskID, spec.DepTaskId)
					return o.Err()
				},
			},
			{
				Name:      "waive",
				Usage:     "Waive a failed hard dependency",
				ArgsUsage: "<task-id> <dep-task-id>",
				Flags:     append(taskAgentFlags(), &ucli.StringFlag{Name: "reason", Usage: "Waiver reason"}, jsonFlag()),
				Action: func(c *ucli.Context) error {
					if c.NArg() < 2 {
						return fmt.Errorf("usage: stella task dep waive <task-id> <dep-task-id>")
					}
					if err := requireTaskAgent(c); err != nil {
						return err
					}
					taskID := c.Args().Get(0)
					depTaskID := c.Args().Get(1)
					body := apitypes.WaiveDepRequest{}
					if r := c.String("reason"); r != "" {
						body.Reason = &r
					}
					if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
						return api.WaiveTaskDep(c.Context, taskID, depTaskID, body)
					}); err != nil {
						return fmt.Errorf("waive dep: %w", err)
					}
					if isJSON(c) {
						return printJSON(c, depResult{TaskID: taskID, DepTaskID: depTaskID, Action: "waived"})
					}
					o := stdout(c)
					o.printf("waived dep %s -> %s\n", taskID, depTaskID)
					return o.Err()
				},
			},
		},
	}
}

func taskBlockerCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "blocker",
		Usage: "Resolve a task blocker",
		Subcommands: []*ucli.Command{
			{
				Name:      "resolve",
				Usage:     "Resolve an open blocker (resume a blocked task)",
				ArgsUsage: "<task-id> <blocker-id>",
				Description: "Get the blocker id from `stella task get` (active_blocker). " +
					"dep_failure blockers must be cleared with `stella task dep waive` instead.",
				Flags: append(taskAgentFlags(), &ucli.StringFlag{Name: "resolution", Usage: "Resolution note / answer"}, jsonFlag()),
				Action: func(c *ucli.Context) error {
					if c.NArg() < 2 {
						return fmt.Errorf("usage: stella task blocker resolve <task-id> <blocker-id>")
					}
					if err := requireTaskAgent(c); err != nil {
						return err
					}
					taskID := c.Args().Get(0)
					blockerID := c.Args().Get(1)
					body := apitypes.ResolveBlockerRequest{}
					if r := c.String("resolution"); r != "" {
						body.Resolution = &r
					}
					task, err := apiclient.Call[apitypes.Task](func(api *apiclient.Client) (*http.Response, error) {
						return api.ResolveTaskBlocker(c.Context, taskID, blockerID, body)
					})
					if err != nil {
						return fmt.Errorf("resolve blocker: %w", err)
					}
					return printTask(c, task)
				},
			},
		},
	}
}

// depResult is the stable JSON shape for dep add/waive, which return 204 with
// no body but still need a scriptable confirmation.
type depResult struct {
	TaskID    string `json:"task_id"`
	DepTaskID string `json:"dep_task_id"`
	Action    string `json:"action"`
}

func printReviewList(c *ucli.Context, items []apitypes.Review) error {
	o := stdout(c)
	for _, r := range items {
		o.printf("%-36s  %-12s  %-8s  %s\n", r.Id, r.Status, r.ReviewerType, r.Feedback)
	}
	return o.Err()
}

func reviewDecisionCmd(verb, usage string) *ucli.Command {
	return &ucli.Command{
		Name:      verb,
		Usage:     usage,
		ArgsUsage: "<task-id> <review-id>",
		Flags: append(taskAgentFlags(),
			&ucli.StringFlag{Name: "summary", Usage: "Decision summary"},
			&ucli.StringFlag{Name: "feedback", Usage: "Feedback to the worker (reject / request-changes)"},
			jsonFlag(),
		),
		Action: func(c *ucli.Context) error {
			if c.NArg() < 2 {
				return fmt.Errorf("usage: stella task review %s <task-id> <review-id>", verb)
			}
			if err := requireTaskAgent(c); err != nil {
				return err
			}
			taskID := c.Args().Get(0)
			reviewID := c.Args().Get(1)
			body := apitypes.ReviewDecisionRequest{}
			if s := c.String("summary"); s != "" {
				body.Summary = &s
			}
			if f := c.String("feedback"); f != "" {
				body.Feedback = &f
			}
			rev, err := apiclient.Call[apitypes.Review](func(api *apiclient.Client) (*http.Response, error) {
				switch verb {
				case "approve":
					return api.ApproveTaskReview(c.Context, taskID, reviewID, body)
				case "reject":
					return api.RejectTaskReview(c.Context, taskID, reviewID, body)
				case "request-changes":
					return api.RequestChangesTaskReview(c.Context, taskID, reviewID, body)
				}
				return nil, fmt.Errorf("unknown verb %q", verb)
			})
			if err != nil {
				return fmt.Errorf("review %s: %w", verb, err)
			}
			if isJSON(c) {
				return printJSON(c, rev)
			}
			o := stdout(c)
			o.printf("review %s -> %s\n", rev.Id, rev.Status)
			return o.Err()
		},
	}
}

// parseDepSpec accepts "<task-id>", "<task-id>:hard", "<task-id>:hard:block".
func parseDepSpec(spec string) (apitypes.DepInput, error) {
	parts := strings.Split(spec, ":")
	out := apitypes.DepInput{DepTaskId: parts[0]}
	if len(parts) > 1 && parts[1] != "" {
		k := apitypes.DepInputKind(parts[1])
		out.Kind = &k
	}
	if len(parts) > 2 && parts[2] != "" {
		f := apitypes.DepInputOnFailure(parts[2])
		out.OnFailure = &f
	}
	return out, nil
}

func printTask(c *ucli.Context, t apitypes.Task) error {
	if isJSON(c) {
		return printJSON(c, t)
	}
	o := stdout(c)
	o.printf("id:       %s\n", t.Id)
	o.printf("title:    %s\n", t.Title)
	o.printf("status:   %s\n", t.Status)
	o.printf("priority: %s\n", t.Priority)
	o.printf("review:   %s\n", t.ReviewPolicy)
	if t.ActiveBlockerId != nil {
		o.printf("active_blocker: %s\n", *t.ActiveBlockerId)
	}
	if t.ActiveReviewId != nil {
		o.printf("active_review:  %s\n", *t.ActiveReviewId)
	}
	if t.ActiveRunId != nil {
		o.printf("active_run:     %s\n", *t.ActiveRunId)
	}
	o.printf("created:  %s\n", t.CreatedAt.Format("2006-01-02 15:04:05"))
	return o.Err()
}
