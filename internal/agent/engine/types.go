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
