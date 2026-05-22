package main

import (
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
		Description: `Create, track, and act on tasks assigned to the agent. Tasks go through
a lifecycle (pending → running → review → done) and support dependencies,
priorities, and human-in-the-loop actions like approve, reject, and respond.`,
		Subcommands: []*ucli.Command{
			taskListCommand(),
			taskGetCommand(),
			taskCreateCommand(),
			taskUpdateCommand(),
			taskDeleteCommand(),
			taskActionCommand(),
			taskEventsCommand(),
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
			params := &apiclient.ListAgentTasksParams{AgentId: agentID}
			if s := c.String("status"); s != "" {
				params.Status = &s
			}
			list, err := apiclient.Call[apiclient.AgentTaskList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTasks(c.Context, params)
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

func taskGetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "get",
		Usage:     "Get task details",
		ArgsUsage: "<task-id>",
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task get <task-id>")
			}
			task, err := apiclient.Call[apiclient.AgentTask](func(api *apiclient.Client) (*http.Response, error) {
				return api.GetAgentTask(c.Context, id)
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
				AgentId: agentID,
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
				return api.CreateAgentTask(c.Context, body)
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
			&ucli.StringFlag{Name: "title", Usage: "New title"},
			&ucli.StringFlag{Name: "description", Usage: "New description"},
			&ucli.StringFlag{Name: "priority", Usage: "New priority: low, medium, high"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task update <task-id>")
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
				return api.UpdateAgentTask(c.Context, id, body)
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
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task delete <task-id>")
			}
			if err := apiclient.Do(func(api *apiclient.Client) (*http.Response, error) {
				return api.DeleteAgentTask(c.Context, id)
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
			&ucli.StringFlag{Name: "type", Usage: "Action type: approve, reject, respond, cancel (required)", Required: true},
			&ucli.StringFlag{Name: "message", Usage: "Message for respond/reject actions"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task action <task-id> --type <type>")
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
				return api.AgentTaskAction(c.Context, id, body)
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
			&ucli.BoolFlag{Name: "json", Usage: "Output as JSON"},
		},
		Action: func(c *ucli.Context) error {
			id := c.Args().First()
			if id == "" {
				return fmt.Errorf("usage: stella task events <task-id>")
			}
			list, err := apiclient.Call[apiclient.AgentTaskEventList](func(api *apiclient.Client) (*http.Response, error) {
				return api.ListAgentTaskEvents(c.Context, id)
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
