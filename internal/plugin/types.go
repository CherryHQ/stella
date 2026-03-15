package plugin

import "context"

// Tool is a plugin-provided tool definition.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Execute     func(ctx context.Context, args map[string]any) (string, error)
}

// EventKind identifies lifecycle events.
type EventKind string

const (
	EventBeforeToolCall EventKind = "before_tool_call"
	EventAfterToolCall  EventKind = "after_tool_call"
	EventSessionStart   EventKind = "session_start"
	EventSessionEnd     EventKind = "session_end"
)

// HookFunc is a lifecycle hook handler.
// For "before" events, returning a non-nil error cancels the action.
// For "after" events, return values are ignored.
type HookFunc func(ctx context.Context, event any) error
