package main

import (
	"fmt"
	"os"

	ucli "github.com/urfave/cli/v2"
)

// taskAgentID is still used by scheduler subcommands to resolve the calling
// agent. Kept here so the rewrite of taskCommand can land without touching
// scheduler.go.
func taskAgentID(c *ucli.Context) (string, error) {
	if a := c.String("agent-id"); a != "" {
		return a, nil
	}
	if a := os.Getenv("STELLA_AGENT_ID"); a != "" {
		return a, nil
	}
	return "", fmt.Errorf("agent ID is required (pass --agent-id or set STELLA_AGENT_ID)")
}

// PHASE 1 STUB — the task CLI is being rewritten against the flat /api/tasks
// surface. Phase 3 of plan
// ~/.agents/sessions/stella/2026-05-28-task-system-v2-flat-api/plan.md
// replaces this with real subcommands backed by the regenerated client.
func taskCommand() *ucli.Command {
	return &ucli.Command{
		Name:     "task",
		Usage:    "Manage durable background tasks (rewrite in progress)",
		Category: "Feature",
		Action: func(c *ucli.Context) error {
			return fmt.Errorf("stella task: CLI rewrite for /api/tasks is in progress; see plan task-system-v2-flat-api")
		},
	}
}
