package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// SessionReuse reuses the same session across job executions (default).
	SessionReuse = "reuse"
	// SessionNew creates a fresh session for each execution.
	SessionNew = "new"
)

// ExecScope constants define how a job runs at execution time.
const (
	// ExecScopeSystem runs once with no user context (system workspace).
	ExecScopeSystem = "system"
	// ExecScopeUser runs once with a specific user's context (UserID must be set).
	ExecScopeUser = "user"
)

const (
	DispatchKindChat     = "chat"
	DispatchKindWorkflow = "workflow"
)

// Schedule defines when a job runs. Exactly one field must be set.
type Schedule struct {
	Cron  string `json:"cron,omitempty"`  // "0 9 * * 1-5"
	Every string `json:"every,omitempty"` // "30m", "2h"
	At    string `json:"at,omitempty"`    // RFC3339: "2024-01-15T14:30:00+08:00"
}

// Job is the persisted job definition.
const (
	JobOwnerUser   = "user"
	JobOwnerPlugin = "plugin"
	JobOwnerSystem = "system"
)

type Job struct {
	ID           string         `json:"id"`
	OwnerKind    string         `json:"owner_kind,omitempty"`
	ExecScope    string         `json:"exec_scope,omitempty"`
	PluginID     string         `json:"plugin_id,omitempty"`
	JobKey       string         `json:"job_key,omitempty"`
	RuntimeName  string         `json:"runtime_name,omitempty"`
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	Schedule     Schedule       `json:"schedule"`
	Message      string         `json:"message,omitempty"`
	Payload      map[string]any `json:"payload,omitempty"`
	DispatchKind string         `json:"dispatch_kind,omitempty"`
	SessionMode  string         `json:"session_mode"` // "reuse" (default) or "new"
	Enabled      bool           `json:"enabled"`
	AgentID      string         `json:"agent_id,omitempty"` // agent to route to (empty = default)
	UserID       string         `json:"user_id,omitempty"`  // user context (empty = none)
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	LastRunAt    *time.Time     `json:"last_run_at,omitempty"`
	LastError    string         `json:"last_error,omitempty"`
}

const (
	RunStatusRunning = "running"
	RunStatusSuccess = "success"
	RunStatusError   = "error"
)

type JobRun struct {
	ID         string     `json:"id"`
	JobID      string     `json:"job_id"`
	SessionID  string     `json:"session_id"`
	UserID     string     `json:"user_id,omitempty"`
	Status     string     `json:"status"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
	Output     string     `json:"output,omitempty"`
}

// RunOutputSink is a context-carried slot the dispatch callback fills with the
// run's final assistant text so the run record can persist it. Mirrors the
// WithRunSessionID pattern: the scheduler owns the run lifecycle, the callback
// owns the agent conversation.
type RunOutputSink struct {
	mu   sync.Mutex
	text string
}

func (s *RunOutputSink) Set(text string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.text = text
	s.mu.Unlock()
}

func (s *RunOutputSink) get() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.text
}

type runOutputSinkKey struct{}

func withRunOutputSink(ctx context.Context, sink *RunOutputSink) context.Context {
	return context.WithValue(ctx, runOutputSinkKey{}, sink)
}

// RunOutputSinkFromContext returns the output sink for the current run, or nil
// when the job was not dispatched through the run lifecycle.
func RunOutputSinkFromContext(ctx context.Context) *RunOutputSink {
	if v, ok := ctx.Value(runOutputSinkKey{}).(*RunOutputSink); ok {
		return v
	}
	return nil
}

type runIDKey struct{}

func WithRunID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, runIDKey{}, id)
}

func RunIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(runIDKey{}).(string); ok {
		return v
	}
	return ""
}

type runSessionIDKey struct{}

func WithRunSessionID(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, runSessionIDKey{}, sid)
}

func RunSessionIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(runSessionIDKey{}).(string); ok {
		return v
	}
	return ""
}

// SessionID returns the stable session identifier for system job executions.
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

// UserSessionID returns a user-scoped session ID for user-owned job executions.
// In reuse mode the ID is stable per user; in new mode a timestamp suffix ensures freshness.
func (j Job) UserSessionID(userID string) string {
	prefix := "scheduler"
	if j.AgentID != "" {
		prefix = j.AgentID + ":" + prefix
	}
	base := fmt.Sprintf("%s:%s:u%s", prefix, j.ID, userID)
	if j.SessionMode == SessionNew {
		return fmt.Sprintf("%s:%d", base, time.Now().UnixNano())
	}
	return base
}
