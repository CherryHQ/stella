package hooks

import (
	"context"
	"time"

	"github.com/vaayne/anna/pkg/ai"
)

// HookPoint identifies where in the engine loop a hook fires.
type HookPoint string

const (
	PreToolCall  HookPoint = "pre_tool_call"
	PostToolCall HookPoint = "post_tool_call"
	PreLLMCall   HookPoint = "pre_llm_call"
	PostLLMCall  HookPoint = "post_llm_call"
)

// HookMeta carries shared metadata available to all hook invocations.
type HookMeta struct {
	SessionID string
	UserID    int64
	AgentID   string
}

// --- PreToolCall ---

// PreToolCallContext is the typed payload for PreToolCall hooks.
type PreToolCallContext struct {
	HookMeta
	ToolName   string
	ToolCallID string
	Arguments  map[string]any // read by hook; rewrite via result
}

// PreToolCallResult tells the engine what to do after a PreToolCall hook.
type PreToolCallResult struct {
	Arguments map[string]any // non-nil = rewritten args for next hook / execution
	Block     bool           // true = skip tool execution
	BlockMsg  string         // synthetic result text when blocked
}

// PreToolCallHook intercepts tool calls before execution.
type PreToolCallHook interface {
	Name() string
	Priority() int
	OnPreToolCall(ctx context.Context, hctx *PreToolCallContext) (PreToolCallResult, error)
}

// --- PostToolCall ---

// PostToolCallContext is the typed payload for PostToolCall hooks.
type PostToolCallContext struct {
	HookMeta
	ToolName   string
	ToolCallID string
	Arguments  map[string]any
	Result     string
	IsError    bool
	Duration   time.Duration
}

// PostToolCallHook observes tool call results after execution.
type PostToolCallHook interface {
	Name() string
	Priority() int
	OnPostToolCall(ctx context.Context, hctx *PostToolCallContext)
}

// --- PreLLMCall ---

// PreLLMCallContext is the typed payload for PreLLMCall hooks.
type PreLLMCallContext struct {
	HookMeta
	Model           string
	System          string
	ToolDefinitions []ai.ToolDefinition
	MessageCount    int // number of messages (avoid copying full history)
}

// PreLLMCallResult carries mutations from a PreLLMCall hook.
type PreLLMCallResult struct {
	System          *string             // non-nil = override system prompt
	ToolDefinitions []ai.ToolDefinition // non-nil = override tool definitions
	Model           *string             // non-nil = override model name
}

// PreLLMCallHook intercepts LLM calls before they are sent.
type PreLLMCallHook interface {
	Name() string
	Priority() int
	OnPreLLMCall(ctx context.Context, hctx *PreLLMCallContext) (PreLLMCallResult, error)
}

// --- PostLLMCall ---

// PostLLMCallContext is the typed payload for PostLLMCall hooks.
type PostLLMCallContext struct {
	HookMeta
	Model            string
	Provider         string
	Usage            ai.Usage
	StopReason       ai.StopReason
	Duration         time.Duration
	TimeToFirstToken time.Duration // zero if no streaming or first token not observed
	Error            error
}

// PostLLMCallHook observes LLM call results (telemetry, cost tracking).
type PostLLMCallHook interface {
	Name() string
	Priority() int
	OnPostLLMCall(ctx context.Context, hctx *PostLLMCallContext)
}

// --- HookPlugin ---

// HookPlugin is the unit of registration. A plugin implements this
// plus one or more of the typed hook interfaces above.
// The registry type-asserts to discover which hook points a plugin supports.
type HookPlugin interface {
	Name() string
	Priority() int // lower = runs first; ties broken by Name() lexicographic
}
