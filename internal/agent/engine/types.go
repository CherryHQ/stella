package engine

import (
	"context"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
)

// ToolFunc executes one tool invocation.
type ToolFunc func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error)

// ToolSet maps tool names to handlers.
type ToolSet map[string]ToolFunc

// LoopConfig configures the agent loop behavior.
type LoopConfig struct {
	Model           ai.Model
	StreamOptions   ai.StreamOptions
	MaxTurns        int
	Tools           ToolSet
	ToolDefinitions []ai.ToolDefinition
	System          string
	Interrupt       <-chan struct{}
	Hooks           *hooks.HookSet // nil = no hooks (backward compatible)
	HookMeta        hooks.HookMeta // shared metadata for hook invocations
}
