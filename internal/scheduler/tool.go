package scheduler

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/vaayne/anna/pkg/tools"
)

var schedulerInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["add", "list", "remove"],
      "description": "The action to perform"
    },
    "name": {
      "type": "string",
      "description": "Name of the job (required for add)"
    },
    "message": {
      "type": "string",
      "description": "The prompt/instruction to execute on schedule (required for add)"
    },
    "cron": {
      "type": "string",
      "description": "Schedule expression (cron format), e.g. '0 9 * * 1-5' for weekdays at 9am (use cron OR every OR at, not combined)"
    },
    "every": {
      "type": "string",
      "description": "Go duration, e.g. '30m', '2h', '24h' (use every OR cron OR at, not combined)"
    },
    "at": {
      "type": "string",
      "description": "RFC3339 timestamp for a one-time job, e.g. '2024-01-15T14:30:00+08:00' (use at OR cron OR every, not combined)"
    },
    "session_mode": {
      "type": "string",
      "enum": ["reuse", "new"],
      "description": "Session behavior: 'reuse' (default) keeps conversation history across executions, 'new' starts a fresh session each time"
    },
    "id": {
      "type": "string",
      "description": "Job ID (required for remove)"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// SchedulerTool exposes scheduler management as an agent tool.
type SchedulerTool struct {
	service *Service
}

// NewTool creates a SchedulerTool backed by the given service.
func NewTool(service *Service) *SchedulerTool {
	return &SchedulerTool{service: service}
}

// SchedulerDefinition returns the tool definition without requiring a live service.
func SchedulerDefinition() tools.Definition {
	return tools.Definition{
		Name:        "scheduler",
		Description: "Manage scheduled tasks. Use action 'add' to create a recurring or one-time job, 'list' to see all jobs, or 'remove' to delete a job. For one-time jobs, use the 'at' field with an RFC3339 timestamp.",
		InputSchema: schedulerInputSchema,
	}
}

// Definition returns the tool definition for the LLM.
func (t *SchedulerTool) Definition() tools.Definition {
	return SchedulerDefinition()
}

// Execute runs the scheduler tool action.
func (t *SchedulerTool) Execute(_ context.Context, args map[string]any) (string, error) {
	action, _ := args["action"].(string)
	switch action {
	case "add":
		return t.add(args)
	case "list":
		return t.list()
	case "remove":
		return t.remove(args)
	default:
		return "", fmt.Errorf("unknown action %q, expected add/list/remove", action)
	}
}

func (t *SchedulerTool) add(args map[string]any) (string, error) {
	name, _ := args["name"].(string)
	message, _ := args["message"].(string)
	cronExpr, _ := args["cron"].(string)
	every, _ := args["every"].(string)

	at, _ := args["at"].(string)
	sessionMode, _ := args["session_mode"].(string)
	sched := Schedule{Cron: cronExpr, Every: every, At: at}
	job, err := t.service.AddJob(name, message, sched, sessionMode)
	if err != nil {
		return "", err
	}

	out, _ := json.MarshalIndent(job, "", "  ")
	return fmt.Sprintf("Job created:\n%s", out), nil
}

func (t *SchedulerTool) list() (string, error) {
	jobs := t.service.ListJobs()
	visible := make([]Job, 0, len(jobs))
	for _, job := range jobs {
		if IsPluginJob(job) {
			continue
		}
		visible = append(visible, job)
	}
	if len(visible) == 0 {
		return "No scheduled jobs.", nil
	}

	out, _ := json.MarshalIndent(visible, "", "  ")
	return string(out), nil
}

func (t *SchedulerTool) remove(args map[string]any) (string, error) {
	id, _ := args["id"].(string)
	if id == "" {
		return "", fmt.Errorf("id is required for remove action")
	}

	if err := t.service.RemoveJob(id); err != nil {
		return "", err
	}
	return fmt.Sprintf("Job %q removed.", id), nil
}
