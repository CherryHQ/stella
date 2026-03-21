package feishutool

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larktask "github.com/larksuite/oapi-sdk-go/v3/service/task/v2"
	"github.com/vaayne/anna/internal/toolspec"
)

var taskInputSchema = func() map[string]any {
	var m map[string]any
	_ = json.Unmarshal([]byte(`{
  "type": "object",
  "properties": {
    "action": {
      "type": "string",
      "enum": ["create_task", "list_tasks", "get_task", "update_task", "complete_task", "create_tasklist", "list_tasklists", "create_subtask", "list_subtasks"],
      "description": "The action to perform"
    },
    "task_guid": {
      "type": "string",
      "description": "Task GUID (required for get/update/complete/create_subtask/list_subtasks)"
    },
    "tasklist_guid": {
      "type": "string",
      "description": "Tasklist GUID (for create_task to assign, or required for list_tasklists tasks)"
    },
    "summary": {
      "type": "string",
      "description": "Task/tasklist title (required for create_task, create_tasklist, create_subtask)"
    },
    "description": {
      "type": "string",
      "description": "Task description"
    },
    "due": {
      "type": "object",
      "properties": {
        "timestamp": {"type": "string", "description": "Due time in ISO 8601 format, e.g. '2024-01-01T18:00:00+08:00'"},
        "is_all_day": {"type": "boolean", "description": "Whether this is an all-day task"}
      },
      "description": "Task due time (millisecond precision internally)"
    },
    "start": {
      "type": "object",
      "properties": {
        "timestamp": {"type": "string", "description": "Start time in ISO 8601 format"},
        "is_all_day": {"type": "boolean"}
      },
      "description": "Task start time"
    },
    "members": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "id": {"type": "string", "description": "Member open_id"},
          "role": {"type": "string", "enum": ["assignee", "follower"], "description": "Member role (default: assignee)"}
        }
      },
      "description": "Task members (assignee=responsible, follower=watcher)"
    },
    "repeat_rule": {
      "type": "string",
      "description": "RRULE recurrence rule for recurring tasks"
    },
    "completed": {
      "type": "boolean",
      "description": "Filter for completed tasks (for list_tasks)"
    },
    "page_size": {
      "type": "number",
      "description": "Page size (default 50, max 100)"
    },
    "page_token": {
      "type": "string",
      "description": "Pagination token"
    }
  },
  "required": ["action"]
}`), &m)
	return m
}()

// TaskTool provides Feishu task management via the Task v2 API.
type TaskTool struct {
	client *Client
}

// NewTaskTool creates a feishu_task tool.
func NewTaskTool(client *Client) *TaskTool {
	return &TaskTool{client: client}
}

func (t *TaskTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name: "feishu_task",
		Description: `Manage Feishu/Lark tasks (v2 API). Uses user token when available.

Actions:
- create_task: Create a task. Requires summary. Optional: description, due, start, members, repeat_rule, tasklist_guid.
- list_tasks: List tasks visible to the current user. Optional: completed, page_size, page_token.
- get_task: Get task details. Requires task_guid.
- update_task: Update a task. Requires task_guid plus fields to update (summary, description, due, start, members, repeat_rule).
- complete_task: Mark a task as completed. Requires task_guid. Sets completed_at to current time.
- create_tasklist: Create a task list. Requires summary.
- list_tasklists: List all task lists.
- create_subtask: Create a subtask under a parent task. Requires task_guid (parent) and summary.
- list_subtasks: List subtasks of a task. Requires task_guid.

Time format: ISO 8601 with timezone, e.g. '2024-01-01T18:00:00+08:00'. Task API uses millisecond timestamps internally.`,
		InputSchema: taskInputSchema,
	}
}

func (t *TaskTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	action := stringArg(args, "action")
	switch action {
	case "create_task":
		return t.createTask(ctx, args)
	case "list_tasks":
		return t.listTasks(ctx, args)
	case "get_task":
		return t.getTask(ctx, args)
	case "update_task":
		return t.updateTask(ctx, args)
	case "complete_task":
		return t.completeTask(ctx, args)
	case "create_tasklist":
		return t.createTasklist(ctx, args)
	case "list_tasklists":
		return t.listTasklists(ctx, args)
	case "create_subtask":
		return t.createSubtask(ctx, args)
	case "list_subtasks":
		return t.listSubtasks(ctx, args)
	default:
		return "", fmt.Errorf("feishu_task: unknown action %q", action)
	}
}

func (t *TaskTool) createTask(ctx context.Context, args map[string]any) (string, error) {
	summary := stringArg(args, "summary")
	if summary == "" {
		return "", fmt.Errorf("feishu_task create_task: summary is required")
	}

	inputTask, err := buildInputTask(args, summary)
	if err != nil {
		return "", fmt.Errorf("feishu_task create_task: %w", err)
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Task.V2.Task.Create(ctx,
			larktask.NewCreateTaskReqBuilder().
				UserIdType("open_id").
				InputTask(inputTask).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create task: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create task: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"task": resp.Data.Task}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task create_task: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) listTasks(ctx context.Context, args map[string]any) (string, error) {
	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larktask.NewListTaskReqBuilder().
			UserIdType("open_id")

		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}
		if v, ok := boolArg(args, "completed"); ok {
			builder.Completed(v)
		}

		resp, err := t.client.Lark().Task.V2.Task.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list tasks: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list tasks: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"tasks":      resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task list_tasks: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) getTask(ctx context.Context, args map[string]any) (string, error) {
	guid := stringArg(args, "task_guid")
	if guid == "" {
		return "", fmt.Errorf("feishu_task get_task: task_guid is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Task.V2.Task.Get(ctx,
			larktask.NewGetTaskReqBuilder().
				TaskGuid(guid).
				UserIdType("open_id").
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("get task: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("get task: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"task": resp.Data.Task}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task get_task: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) updateTask(ctx context.Context, args map[string]any) (string, error) {
	guid := stringArg(args, "task_guid")
	if guid == "" {
		return "", fmt.Errorf("feishu_task update_task: task_guid is required")
	}

	updateTask, updateFields, err := buildUpdateTask(args)
	if err != nil {
		return "", fmt.Errorf("feishu_task update_task: %w", err)
	}
	if len(updateFields) == 0 {
		return "", fmt.Errorf("feishu_task update_task: no fields to update")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		body := larktask.NewPatchTaskReqBodyBuilder().
			Task(updateTask).
			UpdateFields(updateFields).
			Build()

		resp, err := t.client.Lark().Task.V2.Task.Patch(ctx,
			larktask.NewPatchTaskReqBuilder().
				TaskGuid(guid).
				UserIdType("open_id").
				Body(body).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("update task: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("update task: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"task": resp.Data.Task}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task update_task: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) completeTask(ctx context.Context, args map[string]any) (string, error) {
	guid := stringArg(args, "task_guid")
	if guid == "" {
		return "", fmt.Errorf("feishu_task complete_task: task_guid is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		// Use current time as completed_at (milliseconds).
		nowMs := fmt.Sprintf("%d", currentTimeMs())
		task := larktask.NewInputTaskBuilder().
			CompletedAt(nowMs).
			Build()
		body := larktask.NewPatchTaskReqBodyBuilder().
			Task(task).
			UpdateFields([]string{"completed_at"}).
			Build()

		resp, err := t.client.Lark().Task.V2.Task.Patch(ctx,
			larktask.NewPatchTaskReqBuilder().
				TaskGuid(guid).
				UserIdType("open_id").
				Body(body).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("complete task: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("complete task: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"task": resp.Data.Task}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task complete_task: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) createTasklist(ctx context.Context, args map[string]any) (string, error) {
	summary := stringArg(args, "summary")
	if summary == "" {
		return "", fmt.Errorf("feishu_task create_tasklist: summary is required")
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Task.V2.Tasklist.Create(ctx,
			larktask.NewCreateTasklistReqBuilder().
				UserIdType("open_id").
				InputTasklist(larktask.NewInputTasklistBuilder().Name(summary).Build()).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create tasklist: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create tasklist: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"tasklist": resp.Data.Tasklist}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task create_tasklist: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) listTasklists(ctx context.Context, args map[string]any) (string, error) {
	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larktask.NewListTasklistReqBuilder().
			UserIdType("open_id")
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Task.V2.Tasklist.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list tasklists: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list tasklists: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"tasklists":  resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task list_tasklists: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) createSubtask(ctx context.Context, args map[string]any) (string, error) {
	parentGUID := stringArg(args, "task_guid")
	if parentGUID == "" {
		return "", fmt.Errorf("feishu_task create_subtask: task_guid (parent) is required")
	}
	summary := stringArg(args, "summary")
	if summary == "" {
		return "", fmt.Errorf("feishu_task create_subtask: summary is required")
	}

	inputTask, err := buildInputTask(args, summary)
	if err != nil {
		return "", fmt.Errorf("feishu_task create_subtask: %w", err)
	}

	var result any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		resp, err := t.client.Lark().Task.V2.TaskSubtask.Create(ctx,
			larktask.NewCreateTaskSubtaskReqBuilder().
				TaskGuid(parentGUID).
				UserIdType("open_id").
				InputTask(inputTask).
				Build(),
			opts...)
		if err != nil {
			return fmt.Errorf("create subtask: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("create subtask: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		result = map[string]any{"task": resp.Data.Subtask}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task create_subtask: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

func (t *TaskTool) listSubtasks(ctx context.Context, args map[string]any) (string, error) {
	parentGUID := stringArg(args, "task_guid")
	if parentGUID == "" {
		return "", fmt.Errorf("feishu_task list_subtasks: task_guid is required")
	}

	var result map[string]any
	invokeErr := t.client.InvokeWithUserToken(ctx, false, func(ctx context.Context, opts ...larkcore.RequestOptionFunc) error {
		builder := larktask.NewListTaskSubtaskReqBuilder().
			TaskGuid(parentGUID).
			UserIdType("open_id")
		if ps := intArg(args, "page_size"); ps > 0 {
			builder.PageSize(ps)
		}
		if pt := stringArg(args, "page_token"); pt != "" {
			builder.PageToken(pt)
		}

		resp, err := t.client.Lark().Task.V2.TaskSubtask.List(ctx, builder.Build(), opts...)
		if err != nil {
			return fmt.Errorf("list subtasks: %w", err)
		}
		if !resp.Success() {
			return fmt.Errorf("list subtasks: %s", FormatLarkError(resp.Code, resp.Msg))
		}
		hasMore := resp.Data.HasMore != nil && *resp.Data.HasMore
		pt := ""
		if resp.Data.PageToken != nil {
			pt = *resp.Data.PageToken
		}
		result = map[string]any{
			"tasks":      resp.Data.Items,
			"has_more":   hasMore,
			"page_token": pt,
		}
		return nil
	})
	if invokeErr != nil {
		return "", fmt.Errorf("feishu_task list_subtasks: %w", invokeErr)
	}
	return JSONResultFromAny(result)
}

// buildInputTask constructs an InputTask from tool args.
func buildInputTask(args map[string]any, summary string) (*larktask.InputTask, error) {
	builder := larktask.NewInputTaskBuilder().Summary(summary)

	if desc := stringArg(args, "description"); desc != "" {
		builder.Description(desc)
	}
	if due := mapArg(args, "due"); due != nil {
		d, err := buildDue(due)
		if err != nil {
			return nil, fmt.Errorf("invalid due: %w", err)
		}
		builder.Due(d)
	}
	if start := mapArg(args, "start"); start != nil {
		s, err := buildStart(start)
		if err != nil {
			return nil, fmt.Errorf("invalid start: %w", err)
		}
		builder.Start(s)
	}
	if members := sliceArg(args, "members"); len(members) > 0 {
		builder.Members(buildTaskMembers(members))
	}
	if rr := stringArg(args, "repeat_rule"); rr != "" {
		builder.RepeatRule(rr)
	}
	if tl := stringArg(args, "tasklist_guid"); tl != "" {
		builder.Tasklists([]*larktask.TaskInTasklistInfo{
			larktask.NewTaskInTasklistInfoBuilder().TasklistGuid(tl).Build(),
		})
	}

	return builder.Build(), nil
}

// buildUpdateTask constructs an InputTask and update_fields list for patch.
func buildUpdateTask(args map[string]any) (*larktask.InputTask, []string, error) {
	builder := larktask.NewInputTaskBuilder()
	var fields []string

	if v := stringArg(args, "summary"); v != "" {
		builder.Summary(v)
		fields = append(fields, "summary")
	}
	if v := stringArg(args, "description"); v != "" {
		builder.Description(v)
		fields = append(fields, "description")
	}
	if due := mapArg(args, "due"); due != nil {
		d, err := buildDue(due)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid due: %w", err)
		}
		builder.Due(d)
		fields = append(fields, "due")
	}
	if start := mapArg(args, "start"); start != nil {
		s, err := buildStart(start)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid start: %w", err)
		}
		builder.Start(s)
		fields = append(fields, "start")
	}
	if members := sliceArg(args, "members"); len(members) > 0 {
		builder.Members(buildTaskMembers(members))
		fields = append(fields, "members")
	}
	if rr := stringArg(args, "repeat_rule"); rr != "" {
		builder.RepeatRule(rr)
		fields = append(fields, "repeat_rule")
	}

	return builder.Build(), fields, nil
}

func buildDue(m map[string]any) (*larktask.Due, error) {
	ts, _ := m["timestamp"].(string)
	if ts == "" {
		return nil, fmt.Errorf("timestamp is required")
	}
	msVal, err := ParseTimeToUnixMs(ts)
	if err != nil {
		return nil, err
	}
	builder := larktask.NewDueBuilder().Timestamp(strconv.FormatInt(msVal, 10))
	if allDay, ok := m["is_all_day"].(bool); ok {
		builder.IsAllDay(allDay)
	}
	return builder.Build(), nil
}

func buildStart(m map[string]any) (*larktask.Start, error) {
	ts, _ := m["timestamp"].(string)
	if ts == "" {
		return nil, fmt.Errorf("timestamp is required")
	}
	msVal, err := ParseTimeToUnixMs(ts)
	if err != nil {
		return nil, err
	}
	builder := larktask.NewStartBuilder().Timestamp(strconv.FormatInt(msVal, 10))
	if allDay, ok := m["is_all_day"].(bool); ok {
		builder.IsAllDay(allDay)
	}
	return builder.Build(), nil
}

func buildTaskMembers(raw []any) []*larktask.Member {
	var members []*larktask.Member
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			id, _ := m["id"].(string)
			if id == "" {
				continue
			}
			role, _ := m["role"].(string)
			if role == "" {
				role = "assignee"
			}
			members = append(members, larktask.NewMemberBuilder().
				Id(id).
				Type("user").
				Role(role).
				Build())
		}
	}
	return members
}

// currentTimeMs returns the current time in Unix milliseconds.
// Extracted as a package-level var for testability.
var currentTimeMs = time.Now().UnixMilli
