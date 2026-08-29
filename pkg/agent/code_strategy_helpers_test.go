package agent

import (
	"context"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/codemode"
	"github.com/CherryHQ/stella/pkg/hooks"
)

// These wrappers exist only for the tests below. Production Code Mode always
// goes through executeCodeModeCalls, which routes direct bash calls alongside
// the code tool; keeping the narrower entry points here stops them from looking
// like a second production path.

// executeCodeCalls is the focused code-tool executor: no direct tool set, so
// every call in the batch must be the code tool itself.
func executeCodeCalls(ctx context.Context, calls []ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, cb toolCallbacks, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ([]ai.ToolResultMessage, error) {
	return executeCodeModeCalls(ctx, calls, nil, tools, definitions, cb, hs, meta, lifecycle, canonicalize)
}

func executeCodeCall(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer) ai.ToolResultMessage {
	return executeCodeCallWithCallbacks(ctx, call, tools, definitions, toolCallbacks{}, hs, meta, lifecycle, canonicalize)
}

// executeCodeCallWithLimits pins the limits production leaves at their defaults,
// so a test can prove that a timeout exit retains already-committed metadata.
func executeCodeCallWithLimits(ctx context.Context, call ai.ToolCall, tools ToolSet, definitions []ai.ToolDefinition, hs *hooks.HookSet, meta hooks.HookMeta, lifecycle *ToolLifecycle, canonicalize ToolImageCanonicalizer, limits codemode.Limits) ai.ToolResultMessage {
	return executeCodeCallWithLimitsAndCallbacks(ctx, call, tools, definitions, toolCallbacks{}, hs, meta, lifecycle, canonicalize, limits)
}
