package agenttask

import "github.com/CherryHQ/stella/pkg/tools"

// Identity (user, agent) and the project default come from the session context,
// so they are deliberately absent from every schema below.

func objectSchema(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}

func projectProp() map[string]any {
	return strProp("Optional project ID. Defaults to the current session's project; set to scope to a different project you own.")
}

func limitProp() map[string]any { return intProp("Max rows to return (default 50).") }

func taskListDef() tools.Definition {
	return tools.Definition{
		Name:        "task_list",
		Description: "List your tasks (owned by the current agent). Optionally filter by status. Returns id/title/status/priority/project.",
		InputSchema: objectSchema(map[string]any{
			"status":     strProp("Optional status filter, e.g. draft, ready, running, blocked, done, cancelled."),
			"project_id": projectProp(),
			"limit":      limitProp(),
		}),
	}
}

func taskGetDef() tools.Definition {
	return tools.Definition{
		Name:        "task_get",
		Description: "Get one of your tasks by ID.",
		InputSchema: objectSchema(map[string]any{
			"task_id": strProp("Task ID."),
		}, "task_id"),
	}
}

func taskCreateDef() tools.Definition {
	return tools.Definition{
		Name: "task_create",
		Description: "Create a durable background task owned by the current agent. " +
			"IMPORTANT: the task is created in DRAFT and will NOT run unless you set activate:true. " +
			"Use a task (not delegate) for work that must survive restarts, run asynchronously, or block on review/approval.",
		InputSchema: objectSchema(map[string]any{
			"title":       strProp("Short task title (required)."),
			"description": strProp("What the task should accomplish."),
			"activate":    map[string]any{"type": "boolean", "description": "If true, activate immediately (ready to run). Default false leaves it in draft."},
			"goal_id":     strProp("Optional parent goal ID (must belong to the same agent)."),
			"project_id":  projectProp(),
			"deps":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Optional task IDs this task depends on (hard dependencies)."},
		}, "title"),
	}
}

func taskCancelDef() tools.Definition {
	return tools.Definition{
		Name:        "task_cancel",
		Description: "Cancel one of your tasks.",
		InputSchema: objectSchema(map[string]any{
			"task_id": strProp("Task ID."),
			"reason":  strProp("Optional reason for cancellation."),
		}, "task_id"),
	}
}

func taskEventsDef() tools.Definition {
	return tools.Definition{
		Name:        "task_events",
		Description: "List the audit/event trail for one of your tasks (oldest first).",
		InputSchema: objectSchema(map[string]any{
			"task_id": strProp("Task ID."),
			"limit":   limitProp(),
		}, "task_id"),
	}
}

func taskDepsDef() tools.Definition {
	return tools.Definition{
		Name:        "task_deps",
		Description: "List dependency edges (with upstream status) for one of your tasks.",
		InputSchema: objectSchema(map[string]any{
			"task_id": strProp("Task ID."),
			"limit":   limitProp(),
		}, "task_id"),
	}
}

func goalCreateDef() tools.Definition {
	return tools.Definition{
		Name:        "task_goal_create",
		Description: "Create a goal that groups related tasks under one objective (status rolls up from child tasks). Created in draft.",
		InputSchema: objectSchema(map[string]any{
			"title":       strProp("Short goal title (required)."),
			"description": strProp("What the goal aims to achieve."),
			"project_id":  projectProp(),
		}, "title"),
	}
}

func goalListDef() tools.Definition {
	return tools.Definition{
		Name:        "task_goal_list",
		Description: "List your goals (owned by the current agent).",
		InputSchema: objectSchema(map[string]any{
			"limit": limitProp(),
		}),
	}
}

func goalGetDef() tools.Definition {
	return tools.Definition{
		Name:        "task_goal_get",
		Description: "Get one of your goals by ID.",
		InputSchema: objectSchema(map[string]any{
			"goal_id": strProp("Goal ID."),
		}, "goal_id"),
	}
}
