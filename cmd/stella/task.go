package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

// taskAgentID still serves scheduler subcommands (cmd/stella/scheduler.go).
// The task CLI itself no longer takes an agent id positional.
func taskAgentID(c *ucli.Context) (string, error) {
	if a := c.String("agent-id"); a != "" {
		return a, nil
	}
	if a := os.Getenv("STELLA_AGENT_ID"); a != "" {
		return a, nil
	}
	return "", fmt.Errorf("agent ID is required (pass --agent-id or set STELLA_AGENT_ID)")
}

func taskCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "task",
		Usage:    "Manage durable background tasks (flat /api/tasks v2)",
		Category: "Feature",
		Description: `Create, track, and act on tasks owned by the resolved org.
Org comes from the configured profile (X-Stella-Org-ID); tasks no longer
nest under an agent path.`,
		Subcommands: []*ucli.Command{
			taskListCmd(),
			taskGetCmd(),
			taskCreateCmd(),
			taskCancelCmd(),
			taskReopenCmd(),
			taskReadinessCmd(),
			taskEventsCmd(),
			taskDepsCmd(),
			taskReviewCmd(),
		},
	}
}

func taskListCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List tasks in the resolved org",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent", Usage: "Filter by creator agent ID"},
			&ucli.StringFlag{Name: "status", Usage: "Filter by status"},
		},
		Action: func(c *ucli.Context) error {
			params := &apiclient.ListTasksParams{}
			if a := c.String("agent"); a != "" {
				params.AgentId = &a
			}
			if s := c.String("status"); s != "" {
				params.Status = &s
			}
			list, err := apiclient.Call[apitypes.TaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTasks(c.Context, params)
			})
			if err != nil {
				return fmt.Errorf("list tasks: %w", err)
			}
			for _, t := range list.Items {
				fmt.Printf("%-36s  %-10s  %-8s  %s\n", t.Id, t.Status, t.Priority, t.Title)
			}
			return nil
		},
	}
}

func taskGetCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Show a task",
		ArgsUsage: "<task-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			task, err := apiclient.Call[apitypes.Task](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetTask(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("get task: %w", err)
			}
			printTask(task)
			return nil
		},
	}
}

func taskCreateCmd() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a task",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Required: true, Usage: "Task title"},
			&ucli.StringFlag{Name: "description", Usage: "Task description"},
			&ucli.StringFlag{Name: "agent", Usage: "Creator agent ID (optional)"},
			&ucli.StringFlag{Name: "executor", Usage: "Explicit executor agent ID (D13)"},
			&ucli.StringFlag{Name: "priority", Value: "routine", Usage: "routine | urgent"},
			&ucli.StringSliceFlag{Name: "dep", Usage: "Dependency: <task-id>[:kind[:on_failure]]; may be repeated"},
			&ucli.BoolFlag{Name: "activate", Usage: "Activate (draft -> ready) immediately"},
		},
		Action: func(c *ucli.Context) error {
			title := c.String("title")
			body := apitypes.CreateTaskRequest{Title: title}
			if d := c.String("description"); d != "" {
				body.Description = &d
			}
			if a := c.String("agent"); a != "" {
				body.AgentId = &a
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
			printTask(task)
			return nil
		},
	}
}

func taskCancelCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "cancel",
		Usage:     "Cancel a task",
		ArgsUsage: "<task-id>",
		Flags:     []ucli.Flag{&ucli.StringFlag{Name: "reason", Usage: "Cancellation reason"}},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
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
			printTask(task)
			return nil
		},
	}
}

func taskReopenCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "reopen",
		Usage:     "Reopen a done/failed task",
		ArgsUsage: "<task-id>",
		Flags:     []ucli.Flag{&ucli.BoolFlag{Name: "cascade", Usage: "Cascade-reset downstream tasks"}},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
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
			printTask(task)
			return nil
		},
	}
}

func taskReadinessCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "readiness",
		Usage:     "Show task readiness",
		ArgsUsage: "<task-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			rd, err := apiclient.Call[apitypes.Readiness](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetTaskReadiness(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("readiness: %w", err)
			}
			fmt.Printf("state:        %s\n", rd.State)
			fmt.Printf("dispatchable: %t\n", rd.Dispatchable)
			if rd.Reasons != nil {
				for _, r := range *rd.Reasons {
					fmt.Printf("  - %s", r.Type)
					if r.UpstreamId != nil {
						fmt.Printf(" upstream=%s", *r.UpstreamId)
					}
					if r.Detail != nil {
						fmt.Printf(" detail=%s", *r.Detail)
					}
					fmt.Println()
				}
			}
			return nil
		},
	}
}

func taskEventsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "events",
		Usage:     "List task events",
		ArgsUsage: "<task-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			list, err := apiclient.Call[apitypes.EventList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTaskEvents(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("events: %w", err)
			}
			for _, e := range list.Items {
				from := ""
				if e.FromStatus != nil {
					from = *e.FromStatus
				}
				to := ""
				if e.ToStatus != nil {
					to = *e.ToStatus
				}
				fmt.Printf("%-19s  %-18s  %s -> %s\n",
					e.CreatedAt.Format("2006-01-02 15:04:05"), e.EventType, from, to)
			}
			return nil
		},
	}
}

func taskDepsCmd() *ucli.Command {
	return &ucli.Command{
		Name:      "deps",
		Usage:     "List dependency edges",
		ArgsUsage: "<task-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("task id is required")
			}
			list, err := apiclient.Call[apitypes.DepList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTaskDeps(c.Context, id)
			})
			if err != nil {
				return fmt.Errorf("deps: %w", err)
			}
			for _, d := range list.Items {
				up := ""
				if d.UpstreamStatus != nil {
					up = *d.UpstreamStatus
				}
				fmt.Printf("%-36s  %-6s  %-7s  upstream=%s\n", d.DepTaskId, d.DepKind, d.OnFailure, up)
			}
			return nil
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
		},
	}
}

func reviewDecisionCmd(verb, usage string) *ucli.Command {
	return &ucli.Command{
		Name:      verb,
		Usage:     usage,
		ArgsUsage: "<task-id> <review-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "summary", Usage: "Decision summary"},
			&ucli.StringFlag{Name: "feedback", Usage: "Feedback to the worker (reject / request-changes)"},
		},
		Action: func(c *ucli.Context) error {
			if c.NArg() < 2 {
				return fmt.Errorf("usage: stella task review %s <task-id> <review-id>", verb)
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
			fmt.Printf("review %s -> %s\n", rev.Id, rev.Status)
			return nil
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

func printTask(t apitypes.Task) {
	fmt.Printf("id:       %s\n", t.Id)
	fmt.Printf("title:    %s\n", t.Title)
	fmt.Printf("status:   %s\n", t.Status)
	fmt.Printf("priority: %s\n", t.Priority)
	fmt.Printf("review:   %s\n", t.ReviewPolicy)
	fmt.Printf("created:  %s\n", t.CreatedAt.Format("2006-01-02 15:04:05"))
}
