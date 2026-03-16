package engine

import (
	"context"

	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
)

// ToolFunc executes one tool invocation.
type ToolFunc func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error)

// ToolSet maps tool names to handlers.
type ToolSet map[string]ToolFunc

// PluginHookRunner executes plugin lifecycle hooks.
// Defined as an interface here so the engine package does not import internal/plugin.
type PluginHookRunner interface {
	RunHooks(ctx context.Context, event string, data any) error
}

// BeforeToolCallEvent is passed to before_tool_call hooks.
type BeforeToolCallEvent struct {
	ToolName  string         `json:"toolName"`
	Arguments map[string]any `json:"arguments"`
}

// AfterToolCallEvent is passed to after_tool_call hooks.
type AfterToolCallEvent struct {
	ToolName string `json:"toolName"`
	Result   string `json:"result"`
	IsError  bool   `json:"isError"`
}

// SessionEvent is passed to session_start and session_end hooks.
type SessionEvent struct {
	SessionID string `json:"sessionId"`
	Channel   string `json:"channel"`
}

// LoopConfig configures the agent loop behavior.
type LoopConfig struct {
	Model           ai.Model
	StreamOptions   ai.StreamOptions
	MaxTurns        int
	Tools           ToolSet
	ToolDefinitions []toolspec.Definition
	System          string
	Interrupt       <-chan struct{}
	PluginHooks     PluginHookRunner // optional plugin lifecycle hooks
}
