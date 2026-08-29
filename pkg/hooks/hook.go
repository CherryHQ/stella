package hooks

import (
	"context"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
)

// HookPoint identifies where in the engine loop a hook fires.
type HookPoint string

const (
	PreAgentCall   HookPoint = "pre_agent_call"
	PostAgentCall  HookPoint = "post_agent_call"
	PreToolCall    HookPoint = "pre_tool_call"
	PostToolCall   HookPoint = "post_tool_call"
	PreLLMCall     HookPoint = "pre_llm_call"
	PostLLMCall    HookPoint = "post_llm_call"
	PreMemoryCall  HookPoint = "pre_memory_call"
	PostMemoryCall HookPoint = "post_memory_call"
)

// HookMeta carries shared metadata available to all hook invocations.
type HookMeta struct {
	SessionID string
	UserID    string
	AgentID   string
	Channel   string
	BindingID string
}

type (
	toolParentContextKey struct{}
	telemetryMetaKey     struct{}
)

// WithTelemetryMeta carries transport metadata into memory operations without
// coupling the memory package to the agent runtime.
func WithTelemetryMeta(ctx context.Context, channel, bindingID string) context.Context {
	return context.WithValue(ctx, telemetryMetaKey{}, struct{ Channel, BindingID string }{channel, bindingID})
}

func TelemetryMetaFromContext(ctx context.Context) (channel, bindingID string) {
	v, _ := ctx.Value(telemetryMetaKey{}).(struct{ Channel, BindingID string })
	return v.Channel, v.BindingID
}

// WithToolParent marks a context whose current span should parent nested tool
// calls, used by the Code Mode outer call. Ordinary native tools continue to
// use the turn span as their parent.
func WithToolParent(ctx context.Context) context.Context {
	return context.WithValue(ctx, toolParentContextKey{}, true)
}

func HasToolParent(ctx context.Context) bool {
	v, _ := ctx.Value(toolParentContextKey{}).(bool)
	return v
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
	Arguments    map[string]any  // non-nil = rewritten args for next hook / execution
	Block        bool            // true = skip tool execution
	BlockMessage string          // synthetic result text when blocked
	Context      context.Context // non-nil = enriched context for tool execution (e.g. with trace span)
}

// PreToolCallHook intercepts tool calls before execution.
type PreToolCallHook interface {
	Name() string
	Priority() int
	OnPreToolCall(ctx context.Context, hctx *PreToolCallContext) (PreToolCallResult, error)
}

// --- PostToolCall ---

// PostToolCallContext is the typed payload for PostToolCall hooks.
type ChildToolCallAudit struct {
	Count        int
	ErrorCount   int
	FailureClass string
}

type PostToolCallContext struct {
	HookMeta
	ToolName           string
	ToolCallID         string
	Arguments          map[string]any
	Result             string
	IsError            bool
	ChildToolCallAudit ChildToolCallAudit
	// ErrorKind classifies IsError (#1077): the tool itself failed
	// ("tool_error"), exited nonzero ("command_nonzero"), or hit its explicit
	// command timeout ("command_timeout").
	// Empty means unclassified — never assume a kind from IsError alone.
	ErrorKind ai.ToolErrorKind
	// ExitCode is the command's status when ErrorKind is command_nonzero or
	// command_timeout (-1), nil
	// otherwise. A pointer because 0 is a real exit code and must not double
	// as "unknown".
	ExitCode *int
	Duration time.Duration
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
	CallID          string
	Model           string
	System          string
	ToolDefinitions []ai.ToolDefinition
	MessageCount    int // number of messages (avoid copying full history)

	// Model/request metadata for tracing.
	API         string   // provider API key (e.g. "anthropic", "openai")
	Provider    string   // provider display name
	BaseURL     string   // provider endpoint URL
	MaxTokens   *int     // nil = not set
	Temperature *float64 // nil = not set
}

// PreLLMCallResult carries mutations from a PreLLMCall hook.
type PreLLMCallResult struct {
	System          *string             // non-nil = override system prompt
	ToolDefinitions []ai.ToolDefinition // non-nil = override tool definitions
	Model           *string             // non-nil = override model name
	Context         context.Context     // non-nil = enriched context for provider call (e.g. with trace span)
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
	CallID            string
	Model             string
	ProviderToolNames []string
	CodeCatalogSize   int
	Provider          string // provider display name (may be empty)
	API               string // provider API key (e.g. "anthropic", "openai")
	BaseURL           string // provider endpoint URL
	Usage             ai.Usage
	StopReason        ai.StopReason
	Duration          time.Duration
	TimeToFirstToken  time.Duration // zero if no streaming or first token not observed
	// Attempts is the number of provider HTTP requests this call took,
	// including SDK-internal retries. 1 means no retry; 0 means nothing
	// counted them (a stream that never went over HTTP, e.g. a test fake).
	Attempts      int
	ToolCallCount int
	Error         error
}

// PostLLMCallHook observes LLM call results (telemetry, cost tracking).
type PostLLMCallHook interface {
	Name() string
	Priority() int
	OnPostLLMCall(ctx context.Context, hctx *PostLLMCallContext)
}

// --- MemoryCall ---

// MemoryOp identifies the memory operation being traced.
type MemoryOp string

const (
	MemoryOpBootstrap                  MemoryOp = "bootstrap"
	MemoryOpAppend                     MemoryOp = "append"
	MemoryOpAssemble                   MemoryOp = "assemble"
	MemoryOpStats                      MemoryOp = "stats"
	MemoryOpNeedsCompaction            MemoryOp = "needs_compaction"
	MemoryOpCompact                    MemoryOp = "compact"
	MemoryOpSearch                     MemoryOp = "search"
	MemoryOpDescribe                   MemoryOp = "describe"
	MemoryOpExpand                     MemoryOp = "expand"
	MemoryOpGetMessage                 MemoryOp = "get_message"
	MemoryOpGetProfile                 MemoryOp = "get_profile"
	MemoryOpGetProfileAt               MemoryOp = "get_profile_at"
	MemoryOpSetProfile                 MemoryOp = "set_profile"
	MemoryOpGetAgentSoul               MemoryOp = "get_agent_soul"
	MemoryOpGetAgentSoulAt             MemoryOp = "get_agent_soul_at"
	MemoryOpSetAgentSoul               MemoryOp = "set_agent_soul"
	MemoryOpSaveInfo                   MemoryOp = "save_info"
	MemoryOpArchiveInfo                MemoryOp = "archive_info"
	MemoryOpRotateInfo                 MemoryOp = "rotate_info"
	MemoryOpTouchActiveInfo            MemoryOp = "touch_active_info"
	MemoryOpLoadInfo                   MemoryOp = "load_info"
	MemoryOpListInfo                   MemoryOp = "list_info"
	MemoryOpListInfoForReview          MemoryOp = "list_info_for_review"
	MemoryOpLoadHistory                MemoryOp = "load_history"
	MemoryOpLoadReviewHistory          MemoryOp = "load_review_history"
	MemoryOpGetOrCreateSessionSnapshot MemoryOp = "get_or_create_session_snapshot"
	MemoryOpAdvanceSessionSnapshot     MemoryOp = "advance_session_snapshot"
	MemoryOpBuildReview                MemoryOp = "build_review"
)

// PreMemoryCallContext is the typed payload for PreMemoryCall hooks.
// Fired before the memory provider touches storage so hooks can enrich ctx;
// DB spans created inside the operation then nest under the memory span.
type PreMemoryCallContext struct {
	HookMeta
	Op        MemoryOp
	SessionID string // memory session ID, empty for global/user memory ops
}

// PreMemoryCallResult carries mutations from a PreMemoryCall hook.
type PreMemoryCallResult struct {
	Context context.Context // non-nil = enriched context for the memory operation
}

// PreMemoryCallHook intercepts memory operations before storage access.
type PreMemoryCallHook interface {
	Name() string
	Priority() int
	OnPreMemoryCall(ctx context.Context, hctx *PreMemoryCallContext) (PreMemoryCallResult, error)
}

// PostMemoryCallContext is the typed payload for PostMemoryCall hooks.
type PostMemoryCallContext struct {
	HookMeta
	Op        MemoryOp
	SessionID string // memory session ID
	Duration  time.Duration
	Error     error
	// Operation-specific fields (zero values for irrelevant ops).
	MessageCount int // Append: number of messages; Assemble: returned count
	TokenCount   int // Assemble: total tokens; Stats: total tokens; Compact: tokens after
	TokenDelta   int // Compact: tokens saved (negative = reduction)
	SummaryCount int // Compact: summaries created; Stats: total summaries
	ResultCount  int // Search: number of results
	// Detail contains a human-readable dump of the operation's content.
	// Only populated when verbose tracing is enabled. Empty in normal mode.
	// Append: message roles/previews. Assemble: returned messages.
	// Search: query + results. Profile: content. Compact: result breakdown.
	Detail string
}

// PostMemoryCallHook observes memory operations after execution.
type PostMemoryCallHook interface {
	Name() string
	Priority() int
	OnPostMemoryCall(ctx context.Context, hctx *PostMemoryCallContext)
}

// --- PreAgentCall / PostAgentCall ---

// PreAgentCallContext is the typed payload for PreAgentCall hooks.
// Fired when a chat request enters the agent pool, before any memory or LLM work.
type PreAgentCallContext struct {
	HookMeta
	MessageLen int    // length of the user message text
	Channel    string // channel the chat originated from (e.g. "cli", "telegram")
}

// PreAgentCallHook observes the start of a chat request.
type PreAgentCallHook interface {
	Name() string
	Priority() int
	OnPreAgentCall(ctx context.Context, hctx *PreAgentCallContext)
}

// PostAgentCallContext is the typed payload for PostAgentCall hooks.
// Fired when the chat stream completes (all events consumed and persisted).
type PostAgentCallContext struct {
	HookMeta
	Duration time.Duration
	Error    error
}

// PostAgentCallHook observes the completion of a chat request.
type PostAgentCallHook interface {
	Name() string
	Priority() int
	OnPostAgentCall(ctx context.Context, hctx *PostAgentCallContext)
}

// --- HookPlugin ---

// HookPlugin is the unit of registration. A plugin implements this
// plus one or more of the typed hook interfaces above.
// The registry type-asserts to discover which hook points a plugin supports.
type HookPlugin interface {
	Name() string
	Priority() int // lower = runs first; ties broken by Name() lexicographic
}
