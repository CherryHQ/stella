package agentsched

import "github.com/CherryHQ/stella/pkg/tools"

// Identity (user, agent) comes from the session context, so it is deliberately
// absent from every schema below. Exactly one of cron/every/at must be set.

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

func addDef() tools.Definition {
	return tools.Definition{
		Name: "scheduler_add",
		Description: "Schedule a recurring or one-off instruction the current agent runs on your behalf. " +
			"Provide exactly one of cron, every, or at.",
		InputSchema: objectSchema(map[string]any{
			"name":         strProp("Short job name (required)."),
			"message":      strProp("The instruction the agent runs on schedule (required)."),
			"cron":         strProp("Cron expression, e.g. '0 9 * * *' for 09:00 daily."),
			"every":        strProp("Interval duration, e.g. '10m', '1h', '24h'."),
			"at":           strProp("One-off RFC3339 time, e.g. '2026-06-04T09:00:00Z'."),
			"session_mode": strProp("Optional session mode for the scheduled run (e.g. continue, fresh)."),
		}, "name", "message"),
	}
}

func listDef() tools.Definition {
	return tools.Definition{
		Name:        "scheduler_list",
		Description: "List your scheduled jobs (owned by the current user and agent).",
		InputSchema: objectSchema(map[string]any{}),
	}
}

func removeDef() tools.Definition {
	return tools.Definition{
		Name:        "scheduler_remove",
		Description: "Remove one of your scheduled jobs by ID. Plugin- and system-owned jobs are read-only.",
		InputSchema: objectSchema(map[string]any{
			"id": strProp("Job ID."),
		}, "id"),
	}
}
