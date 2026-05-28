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

// taskAgentID resolves an agent ID from --agent-id or $STELLA_AGENT_ID. Kept
// here because sibling CLI commands (e.g. scheduler) still need an agent-
// scoped flag even though the v2 task API is org-scoped.
func taskAgentID(c *ucli.Context) (string, error) {
	if a := c.String("agent-id"); a != "" {
		return a, nil
	}
	if a := os.Getenv("STELLA_AGENT_ID"); a != "" {
		return a, nil
	}
	return "", fmt.Errorf("agent ID is required (pass --agent-id or set STELLA_AGENT_ID)")
}

func printTaskJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// taskCommand is the CLI surface for the v2 task system. Slice 1 (PR 3) ships
// list / get / create / cancel / readiness — matching the API. The old action
// / update / delete / events subcommands belonged to the v1 API and will be
// reintroduced as endpoints land in later PRs (review decision, blocker
// resolve, etc.).
func taskCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "task",
		Usage:    "Manage durable background tasks (task system v2)",
		Category: "Feature",
		Description: `Tasks go through a lifecycle (draft → ready → running → done|failed|cancelled).
The dispatcher claims ready tasks, runs them via the assigned agent, and records
each attempt as a run. Use the readiness subcommand to see why a task is or isn't
currently dispatchable.`,
		Subcommands: []*ucli.Command{
			taskListCommand(),
			taskGetCommand(),
			taskCreateCommand(),
			taskCancelCommand(),
			taskReadinessCommand(),
		},
	}
}

func resolveOrgID(c *ucli.Context) (string, error) {
	if v := c.String("org-id"); v != "" {
		return v, nil
	}
	if v := os.Getenv("STELLA_ORG_ID"); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("org ID required (--org-id or STELLA_ORG_ID)")
}

func taskListCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "list",
		Usage: "List tasks for the caller's org",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "org-id", Usage: "Org ID (defaults to STELLA_ORG_ID)"},
			&ucli.IntFlag{Name: "limit", Value: 50},
			&ucli.IntFlag{Name: "offset", Value: 0},
			&ucli.BoolFlag{Name: "json"},
		},
		Action: func(c *ucli.Context) error {
			_, err := resolveOrgID(c)
			if err != nil {
				return err
			}
			limit := c.Int("limit")
			offset := c.Int("offset")
			params := &apiclient.ListTasksParams{Limit: &limit, Offset: &offset}
			list, err := apiclient.Call[apitypes.AgentTaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListTasks(c.Context, params)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printTaskJSON(list)
			}
			if len(list.Items) == 0 {
				fmt.Println("(no tasks)")
				return nil
			}
			fmt.Printf("%-36s  %-10s  %-8s  %s\n", "ID", "STATUS", "PRIORITY", "TITLE")
			for _, t := range list.Items {
				fmt.Printf("%-36s  %-10s  %-8s  %s\n", t.Id, t.Status, t.Priority, t.Title)
			}
			return nil
		},
	}
}

func taskGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Get a task by ID",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "org-id"},
			&ucli.BoolFlag{Name: "json"},
		},
		Action: func(c *ucli.Context) error {
			if c.NArg() != 1 {
				return fmt.Errorf("task ID required")
			}
			id := c.Args().First()
			_, err := resolveOrgID(c)
			if err != nil {
				return err
			}
			task, err := apiclient.Call[apitypes.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetTask(c.Context, id)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printTaskJSON(task)
			}
			printTask(task)
			return nil
		},
	}
}

func taskCreateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "create",
		Usage: "Create a task",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "org-id"},
			&ucli.StringFlag{Name: "title", Required: true},
			&ucli.StringFlag{Name: "description"},
			&ucli.StringFlag{Name: "priority", Value: "routine", Usage: "routine | urgent"},
			&ucli.StringFlag{Name: "executor-agent-id"},
			&ucli.BoolFlag{Name: "activate", Usage: "Move directly to ready instead of staying in draft"},
			&ucli.BoolFlag{Name: "json"},
		},
		Action: func(c *ucli.Context) error {
			_, err := resolveOrgID(c)
			if err != nil {
				return err
			}
			body := apiclient.CreateTaskJSONRequestBody{
				Title: c.String("title"),
			}
			if d := c.String("description"); d != "" {
				body.Description = &d
			}
			if p := c.String("priority"); p != "" {
				pp := apitypes.AgentTaskInputPriority(p)
				body.Priority = &pp
			}
			if e := c.String("executor-agent-id"); e != "" {
				body.ExecutorAgentId = &e
			}
			if c.Bool("activate") {
				a := true
				body.Activate = &a
			}
			task, err := apiclient.Call[apitypes.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.CreateTask(c.Context, body)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printTaskJSON(task)
			}
			printTask(task)
			return nil
		},
	}
}

func taskCancelCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "cancel",
		Usage:     "Cancel a task",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "org-id"},
			&ucli.StringFlag{Name: "reason"},
		},
		Action: func(c *ucli.Context) error {
			if c.NArg() != 1 {
				return fmt.Errorf("task ID required")
			}
			id := c.Args().First()
			_, err := resolveOrgID(c)
			if err != nil {
				return err
			}
			body := apiclient.CancelTaskJSONRequestBody{}
			if r := c.String("reason"); r != "" {
				body.Reason = &r
			}
			task, err := apiclient.Call[apitypes.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.CancelTask(c.Context, id, body)
			})
			if err != nil {
				return err
			}
			fmt.Printf("Cancelled task %s (status=%s)\n", task.Id, task.Status)
			return nil
		},
	}
}

func taskReadinessCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "readiness",
		Usage:     "Inspect why a task is or isn't dispatchable",
		ArgsUsage: "<task-id>",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "org-id"},
			&ucli.BoolFlag{Name: "json"},
		},
		Action: func(c *ucli.Context) error {
			if c.NArg() != 1 {
				return fmt.Errorf("task ID required")
			}
			id := c.Args().First()
			_, err := resolveOrgID(c)
			if err != nil {
				return err
			}
			rd, err := apiclient.Call[apitypes.AgentTaskReadiness](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetTaskReadiness(c.Context, id)
			})
			if err != nil {
				return err
			}
			if c.Bool("json") {
				return printTaskJSON(rd)
			}
			fmt.Printf("state=%s dispatchable=%v\n", rd.State, rd.Dispatchable)
			if rd.Reasons != nil {
				for _, r := range *rd.Reasons {
					reason := r.Type
					if r.UpstreamId != nil {
						reason += " upstream=" + *r.UpstreamId
					}
					if r.Detail != nil {
						reason += " " + *r.Detail
					}
					fmt.Println("  -", reason)
				}
			}
			return nil
		},
	}
}

func printTask(t apitypes.AgentTask) {
	fmt.Printf("ID:        %s\n", t.Id)
	fmt.Printf("Title:     %s\n", t.Title)
	fmt.Printf("Status:    %s\n", t.Status)
	fmt.Printf("Priority:  %s\n", t.Priority)
	if t.Description != nil && *t.Description != "" {
		fmt.Printf("Desc:      %s\n", strings.TrimSpace(*t.Description))
	}
	if t.AgentId != nil {
		fmt.Printf("Agent:     %s\n", *t.AgentId)
	}
	if t.RetryCount != nil && t.MaxRetries != nil {
		fmt.Printf("Retries:   %d/%d\n", *t.RetryCount, *t.MaxRetries)
	}
	fmt.Printf("Created:   %s\n", t.CreatedAt.Format(time.RFC3339))
	fmt.Printf("Updated:   %s\n", t.UpdatedAt.Format(time.RFC3339))
}
