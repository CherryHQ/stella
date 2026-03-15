package plugin

import (
	"context"
	"log/slog"
)

// Factory is registered by Go plugins in init(). The Manager instantiates
// plugins from factories after config is loaded at runtime.
type Factory struct {
	Name string
	New  func(cfg map[string]any) (Plugin, error)
}

// Plugin is the interface both Go and JS plugins implement.
type Plugin interface {
	Name() string
	Init(ctx Context) error
	Close() error
}

// Context is passed to Plugin.Init(), providing registration APIs.
type Context struct {
	Config       map[string]any
	Logger       *slog.Logger
	RegisterTool func(Tool) error
	OnEvent      func(EventKind, HookFunc)
}

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

// BeforeToolCallEvent is passed to EventBeforeToolCall hooks.
type BeforeToolCallEvent struct {
	ToolName  string         `json:"toolName"`
	Arguments map[string]any `json:"arguments"`
}

// AfterToolCallEvent is passed to EventAfterToolCall hooks.
type AfterToolCallEvent struct {
	ToolName string `json:"toolName"`
	Result   string `json:"result"`
	IsError  bool   `json:"isError"`
}

// SessionEvent is passed to EventSessionStart and EventSessionEnd hooks.
type SessionEvent struct {
	SessionID string `json:"sessionId"`
	Channel   string `json:"channel"`
}
