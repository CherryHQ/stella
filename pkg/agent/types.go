package agent

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// ToolFunc executes one tool invocation.
type ToolFunc func(ctx context.Context, call ai.ToolCall) (ai.TextContent, error)

// ToolSet maps tool names to handlers.
type ToolSet map[string]ToolFunc

// loopConfig configures the agent loop behavior.
type loopConfig struct {
	Model           ai.Model
	StreamOptions   ai.StreamOptions
	MaxTurns        int
	Tools           ToolSet
	ToolDefinitions []ai.ToolDefinition
	System          string
	Interrupt       <-chan struct{}
	Hooks           *hooks.HookSet
	HookMeta        hooks.HookMeta
	ToolLifecycle   *ToolLifecycle
}
