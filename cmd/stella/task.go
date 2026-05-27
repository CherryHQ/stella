package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"

	apiclient "github.com/CherryHQ/stella/api/client"
	apitypes "github.com/CherryHQ/stella/api/types"
)

func taskCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "task",
		Usage:    "Manage durable background tasks with lifecycle tracking and human-in-the-loop actions",
		Category: "Feature",
		Description: `Create, track, and act on tasks and goals assigned to the agent. Goals
are parent containers that split into child tasks with dependencies, acceptance
criteria, and review gates.`,
		Subcommands: []*ucli.Command{
			taskListCommand(),
			taskGetCommand(),
			taskCreateCommand(),
			taskUpdateCommand(),
			taskDeleteCommand(),
			taskActionCommand(),
			taskEventsCommand(),
			taskDepsCommand(),
			taskUnblockedCommand(),
			taskBatchCreateCommand(),
			taskRunsCommand(),
			taskReviewsCommand(),
			taskCriteriaCommand(),
			taskSplitCommand(),
			taskPlanReadyCommand(),
			taskReopenCommand(),
		},
	}
}

func taskAgentID(c *ucli.Context) (string, error) {
	if a := c.String("agent-id"); a != "" {
		return a, nil
	}
	if a := os.Getenv("STELLA_AGENT_ID"); a != "" {
		return a, nil
	}
	return "", fmt.Errorf("agent ID is required (pass --agent-id or set STELLA_AGENT_ID)")
}

func taskListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List tasks",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringFlag{Name: "status", Usage: "Filter by status (pending, running, blocked, review, done, cancelled, failed)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			params := &apiclient.ListAgentTasksParams{}
			if s := c.String("status"); s != "" {
				params.Status = &s
			}
			list, err := apiclient.Call[apiclient.AgentTaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTasks(c.Context, agentID, params)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No tasks.")
				return nil
			}
			fmt.Printf("%-10s  %-5s  %-30s  %-18s  %-8s  %s\n", "ID", "TYPE", "TITLE", "STATUS", "PRIORITY", "UPDATED")
			for _, t := range list.Items {
				fmt.Printf("%-10s  %-5s  %-30s  %-18s  %-8s  %s\n",
					shortID(t.Id),
					string(t.TaskType),
					truncate(t.Title, 30),
					string(t.Status),
					string(t.Priority),
					t.UpdatedAt.Format(time.DateTime))
			}
			return nil
		},
	}
}

func taskGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Get task details",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task get <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetAgentTask(c.Context, agentID, id)
			})
			if err != nil {
				return err
			}
			return printJSON(task)
		},
	}
}

func taskCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a new task",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "Task title (required)", Required: true},
			&ucli.StringFlag{Name: "description", Usage: "Task description"},
			&ucli.StringFlag{Name: "priority", Usage: "Priority: low, medium, high (default: medium)"},
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringSliceFlag{Name: "dep", Usage: "Dependency task ID (can be repeated)"},
		},
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			body := apiclient.CreateAgentTaskJSONRequestBody{
				Title:   c.String("title"),
				AgentId: &agentID,
			}
			if d := c.String("description"); d != "" {
				body.Description = &d
			}
			if p := c.String("priority"); p != "" {
				prio := apitypes.AgentTaskInputPriority(p)
				body.Priority = &prio
			}
			if deps := c.StringSlice("dep"); len(deps) > 0 {
				body.Deps = &deps
			}
			task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateAgentTask(c.Context, agentID, body)
			})
			if err != nil {
				return err
			}
			return printJSON(task)
		},
	}
}

func taskUpdateCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "update",
		Usage:     "Update a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringFlag{Name: "title", Usage: "New title"},
			&ucli.StringFlag{Name: "description", Usage: "New description"},
			&ucli.StringFlag{Name: "priority", Usage: "New priority: low, medium, high"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task update <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			body := apiclient.UpdateAgentTaskJSONRequestBody{}
			if t := c.String("title"); t != "" {
				body.Title = &t
			}
			if d := c.String("description"); d != "" {
				body.Description = &d
			}
			if p := c.String("priority"); p != "" {
				prio := apitypes.AgentTaskUpdatePriority(p)
				body.Priority = &prio
			}
			task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.UpdateAgentTask(c.Context, agentID, id, body)
			})
			if err != nil {
				return err
			}
			return printJSON(task)
		},
	}
}

func taskDeleteCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "delete",
		Usage:     "Delete a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task delete <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteAgentTask(c.Context, agentID, id)
			}); err != nil {
				return err
			}
			fmt.Printf("Task %q deleted.\n", id)
			return nil
		},
	}
}

func taskActionCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "action",
		Usage:     "Take an action on a task (approve, reject, respond, cancel)",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringFlag{Name: "type", Usage: "Action type: approve, reject, respond, cancel (required)", Required: true},
			&ucli.StringFlag{Name: "message", Usage: "Message for respond/reject actions"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task action <task-id> --type <type>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			actionType := c.String("type")
			valid := map[string]bool{"approve": true, "reject": true, "respond": true, "cancel": true}
			if !valid[actionType] {
				return fmt.Errorf("invalid action type %q; must be one of: %s", actionType, strings.Join([]string{"approve", "reject", "respond", "cancel"}, ", "))
			}
			body := apiclient.AgentTaskActionJSONRequestBody{
				Type: apitypes.AgentTaskActionType(actionType),
			}
			if m := c.String("message"); m != "" {
				body.Message = &m
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.AgentTaskAction(c.Context, agentID, id, body)
			}); err != nil {
				return err
			}
			fmt.Printf("Action %q applied to task %s.\n", actionType, shortID(id))
			return nil
		},
	}
}

func taskEventsCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "events",
		Usage:     "List events for a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task events <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apiclient.AgentTaskEventList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTaskEvents(c.Context, agentID, id)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No events.")
				return nil
			}
			fmt.Printf("%-10s  %-20s  %s\n", "ID", "TYPE", "CREATED")
			for _, e := range list.Items {
				fmt.Printf("%-10s  %-20s  %s\n",
					shortID(e.Id),
					string(e.EventType),
					e.CreatedAt.Format(time.DateTime))
			}
			return nil
		},
	}
}

func taskDepsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "deps",
		Usage: "Manage task dependencies",
		Subcommands: []*ucli.Command{
			{
				Name:      "list",
				Usage:     "List upstream and downstream dependencies",
				ArgsUsage: "<task-id>",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
					&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
				},
				Action: func(c *ucli.Context) error {
					id := c.Args().First()
					if id == "" {
						return fmt.Errorf("usage: stella task deps list <task-id>")
					}
					agentID, err := taskAgentID(c)
					if err != nil {
						return err
					}
					info, err := apiclient.Call[apiclient.AgentTaskDepsInfo](func(api *apiclient.Client) (*http.Response, error) {
						return api.GetAgentTaskDeps(c.Context, agentID, id)
					})
					if err != nil {
						return err
					}
					if c.Bool("json") {
						return printJSON(info)
					}
					fmt.Println("Upstream (depends on):")
					if len(info.Upstream) == 0 {
						fmt.Println("  (none)")
					} else {
						for _, t := range info.Upstream {
							fmt.Printf("  %-10s  %-30s  %s\n", shortID(t.Id), truncate(t.Title, 30), string(t.Status))
						}
					}
					fmt.Println("\nDownstream (depended by):")
					if len(info.Downstream) == 0 {
						fmt.Println("  (none)")
					} else {
						for _, t := range info.Downstream {
							fmt.Printf("  %-10s  %-30s  %s\n", shortID(t.Id), truncate(t.Title, 30), string(t.Status))
						}
					}
					return nil
				},
			},
			{
				Name:      "add",
				Usage:     "Add a dependency",
				ArgsUsage: "<task-id>",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
					&ucli.StringFlag{Name: "dep", Usage: "Dependency task ID (required)", Required: true},
				},
				Action: func(c *ucli.Context) error {
					id := c.Args().First()
					if id == "" {
						return fmt.Errorf("usage: stella task deps add <task-id> --dep <dep-id>")
					}
					agentID, err := taskAgentID(c)
					if err != nil {
						return err
					}
					body := apiclient.AddAgentTaskDepJSONRequestBody{
						DepId: c.String("dep"),
					}
					task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
						return api.AddAgentTaskDep(c.Context, agentID, id, body)
					})
					if err != nil {
						return err
					}
					return printJSON(task)
				},
			},
			{
				Name:      "remove",
				Usage:     "Remove a dependency",
				ArgsUsage: "<task-id>",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
					&ucli.StringFlag{Name: "dep", Usage: "Dependency task ID to remove (required)", Required: true},
				},
				Action: func(c *ucli.Context) error {
					id := c.Args().First()
					if id == "" {
						return fmt.Errorf("usage: stella task deps remove <task-id> --dep <dep-id>")
					}
					agentID, err := taskAgentID(c)
					if err != nil {
						return err
					}
					task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
						return api.RemoveAgentTaskDep(c.Context, agentID, id, c.String("dep"))
					})
					if err != nil {
						return err
					}
					return printJSON(task)
				},
			},
		},
	}
}

func taskUnblockedCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "unblocked",
		Usage: "List tasks ready to run (all dependencies done)",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apiclient.AgentTaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListUnblockedAgentTasks(c.Context, agentID)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No unblocked tasks.")
				return nil
			}
			fmt.Printf("%-10s  %-30s  %-10s  %-8s  %s\n", "ID", "TITLE", "STATUS", "PRIORITY", "UPDATED")
			for _, t := range list.Items {
				fmt.Printf("%-10s  %-30s  %-10s  %-8s  %s\n",
					shortID(t.Id),
					truncate(t.Title, 30),
					string(t.Status),
					string(t.Priority),
					t.UpdatedAt.Format(time.DateTime))
			}
			return nil
		},
	}
}

func taskBatchCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "batch-create",
		Usage: "Create multiple tasks from a JSON file",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "JSON file path (required)", Required: true},
		},
		Action: func(c *ucli.Context) error {
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(c.String("file"))
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			var body apiclient.BatchCreateAgentTasksJSONRequestBody
			if err := json.Unmarshal(data, &body); err != nil {
				return fmt.Errorf("parse JSON: %w", err)
			}
			list, err := apiclient.Call[apiclient.AgentTaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.BatchCreateAgentTasks(c.Context, agentID, body)
			})
			if err != nil {
				return err
			}
			return printJSON(list.Items)
		},
	}
}

func taskRunsCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "runs",
		Usage:     "List runs for a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task runs <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.AgentTaskRunList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTaskRuns(c.Context, agentID, id)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No runs.")
				return nil
			}
			fmt.Printf("%-10s  %-12s  %-12s  %-10s  %s\n", "ID", "KIND", "PURPOSE", "STATUS", "CREATED")
			for _, r := range list.Items {
				fmt.Printf("%-10s  %-12s  %-12s  %-10s  %s\n",
					shortID(r.Id),
					string(r.Kind),
					string(r.Purpose),
					string(r.Status),
					r.CreatedAt.Format(time.DateTime))
			}
			return nil
		},
	}
}

func taskReviewsCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "reviews",
		Usage:     "List reviews for a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task reviews <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.AgentTaskReviewList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTaskReviews(c.Context, agentID, id)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No reviews.")
				return nil
			}
			fmt.Printf("%-10s  %-10s  %-18s  %s\n", "ID", "TYPE", "STATUS", "CREATED")
			for _, r := range list.Items {
				fmt.Printf("%-10s  %-10s  %-18s  %s\n",
					shortID(r.Id),
					string(r.ReviewerType),
					string(r.Status),
					r.CreatedAt.Format(time.DateTime))
			}
			return nil
		},
	}
}

func taskCriteriaCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "criteria",
		Usage:     "List acceptance criteria for a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task criteria <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			list, err := apiclient.Call[apitypes.AgentTaskAcceptanceCriterionList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTaskCriteria(c.Context, agentID, id)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printJSON(list.Items)
			}
			if len(list.Items) == 0 {
				fmt.Println("No acceptance criteria.")
				return nil
			}
			for _, cr := range list.Items {
				reqStr := ""
				if cr.Required {
					reqStr = " [required]"
				}
				fmt.Printf("  %d. %s%s\n", cr.Position, cr.Description, reqStr)
			}
			return nil
		},
	}
}

func taskSplitCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "split",
		Usage:     "Split a goal into child tasks from a JSON file",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
			&ucli.StringFlag{Name: "file", Aliases: []string{"f"}, Usage: "JSON file with children (required)", Required: true},
		},
		Action: func(c *ucli.Context) error {
			goalID := c.Args().First()
			if goalID == "" {
				return fmt.Errorf("usage: stella task split <goal-id> --file <json>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(c.String("file"))
			if err != nil {
				return fmt.Errorf("read file: %w", err)
			}
			var body apiclient.SplitGoalIntoTasksJSONRequestBody
			if err := json.Unmarshal(data, &body); err != nil {
				return fmt.Errorf("parse JSON: %w", err)
			}
			list, err := apiclient.Call[apiclient.AgentTaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.SplitGoalIntoTasks(c.Context, agentID, goalID, body)
			})
			if err != nil {
				return err
			}
			return printJSON(list.Items)
		},
	}
}

func taskPlanReadyCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "plan-ready",
		Usage:     "Activate a goal and its draft children",
		ArgsUsage: "<goal-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
		},
		Action: func(c *ucli.Context) error {
			goalID := c.Args().First()
			if goalID == "" {
				return fmt.Errorf("usage: stella task plan-ready <goal-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.PlanReady(c.Context, agentID, goalID)
			})
			if err != nil {
				return err
			}
			return printJSON(task)
		},
	}
}

func taskReopenCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "reopen",
		Usage:     "Reopen a completed or failed task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "agent-id", Usage: "Agent ID (defaults to STELLA_AGENT_ID)"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task reopen <task-id>")
			}
			agentID, err := taskAgentID(c)
			if err != nil {
				return err
			}
			task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.ReopenAgentTask(c.Context, agentID, id)
			})
			if err != nil {
				return err
			}
			return printJSON(task)
		},
	}
}
