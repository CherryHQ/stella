package scheduler

import (
	"fmt"
	"time"
)

const (
	// SessionReuse reuses the same session across job executions (default).
	SessionReuse = "reuse"
	// SessionNew creates a fresh session for each execution.
	SessionNew = "new"
)

// Schedule defines when a job runs. Exactly one field must be set.
type Schedule struct {
	Cron  string `json:"cron,omitempty"`  // "0 9 * * 1-5"
	Every string `json:"every,omitempty"` // "30m", "2h"
	At    string `json:"at,omitempty"`    // RFC3339: "2024-01-15T14:30:00+08:00"
}

// Job is the persisted job definition.
type Job struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Schedule    Schedule  `json:"schedule"`
	Message     string    `json:"message"`
	SessionMode string    `json:"session_mode"` // "reuse" (default) or "new"
	Enabled     bool      `json:"enabled"`
	AgentID     string    `json:"agent_id,omitempty"` // agent to route to (empty = default)
	UserID      int64     `json:"user_id,omitempty"`  // user context (0 = none)
	CreatedAt   time.Time `json:"created_at"`
}

// SessionID returns the session identifier for this job execution.
// Includes agent_id prefix when set for proper scoping.
// In "reuse" mode, the ID is stable across executions. In "new" mode,
// a timestamp suffix ensures each execution gets a fresh session.
func (j Job) SessionID() string {
	prefix := "scheduler"
	if j.AgentID != "" {
		prefix = j.AgentID + ":" + prefix
	}
	base := prefix + ":" + j.ID
	if j.SessionMode == SessionNew {
		return fmt.Sprintf("%s:%d", base, time.Now().UnixNano())
	}
	return base
}
