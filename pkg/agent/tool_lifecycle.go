package agent

import (
	"context"
	"maps"
	"time"
)

// ToolLifecycle carries optional tool-call lifecycle hooks for the agent loop.
type ToolLifecycle struct {
	OperationCheck func(context.Context) error
	BeforeCall     func(ctx context.Context, call ToolCallContext) (ToolCallMutation, error)
	AfterCall      func(ctx context.Context, result ToolResultContext) (ToolResultMutation, error)
}

// ToolCallContext describes one tool call before execution.
type ToolCallContext struct {
	SessionID  string
	Channel    string
	UserID     string
	AgentID    string
	ToolName   string
	ToolCallID string
	Arguments  map[string]any
}

// ToolCallMutation describes how a pre-tool lifecycle hook changes execution.
type ToolCallMutation struct {
	Arguments    map[string]any
	Block        bool
	BlockMessage string
}

// ToolResultContext describes one completed tool call.
type ToolResultContext struct {
	SessionID  string
	Channel    string
	UserID     string
	AgentID    string
	ToolName   string
	ToolCallID string
	Arguments  map[string]any
	Result     string
	IsError    bool
	Duration   time.Duration
}

// ToolResultMutation describes how a post-tool lifecycle hook changes the final result.
type ToolResultMutation struct {
	Result  *string
	IsError *bool
}

func cloneArgs(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	maps.Copy(out, src)
	return out
}
