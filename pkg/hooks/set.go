package hooks

import (
	"context"
	"log/slog"
	"sort"
)

// HookSet holds pre-sorted hook slices for each hook point.
// It is immutable after construction and safe for concurrent use.
type HookSet struct {
	preAgentCall   []PreAgentCallHook
	postAgentCall  []PostAgentCallHook
	preToolCall    []PreToolCallHook
	postToolCall   []PostToolCallHook
	preLLMCall     []PreLLMCallHook
	postLLMCall    []PostLLMCallHook
	preMemoryCall  []PreMemoryCallHook
	postMemoryCall []PostMemoryCallHook
	operationCheck func(context.Context) error
}

// NewHookSet builds a HookSet from a slice of HookPlugins.
// Plugins are sorted by Priority (ascending), then Name (lexicographic).
// Each plugin is type-asserted into the hook point slices it implements.
func NewHookSet(plugins []HookPlugin) *HookSet {
	sorted := make([]HookPlugin, len(plugins))
	copy(sorted, plugins)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Priority() != sorted[j].Priority() {
			return sorted[i].Priority() < sorted[j].Priority()
		}
		return sorted[i].Name() < sorted[j].Name()
	})

	hs := &HookSet{}
	for _, p := range sorted {
		if h, ok := p.(PreAgentCallHook); ok {
			hs.preAgentCall = append(hs.preAgentCall, h)
		}
		if h, ok := p.(PostAgentCallHook); ok {
			hs.postAgentCall = append(hs.postAgentCall, h)
		}
		if h, ok := p.(PreToolCallHook); ok {
			hs.preToolCall = append(hs.preToolCall, h)
		}
		if h, ok := p.(PostToolCallHook); ok {
			hs.postToolCall = append(hs.postToolCall, h)
		}
		if h, ok := p.(PreLLMCallHook); ok {
			hs.preLLMCall = append(hs.preLLMCall, h)
		}
		if h, ok := p.(PostLLMCallHook); ok {
			hs.postLLMCall = append(hs.postLLMCall, h)
		}
		if h, ok := p.(PreMemoryCallHook); ok {
			hs.preMemoryCall = append(hs.preMemoryCall, h)
		}
		if h, ok := p.(PostMemoryCallHook); ok {
			hs.postMemoryCall = append(hs.postMemoryCall, h)
		}
	}
	return hs
}

// WithOperationCheck returns an immutable copy that revalidates ownership
// immediately before each plugin callback. The original HookSet remains safe
// for callers outside a durably owned operation.
func (hs *HookSet) WithOperationCheck(check func(context.Context) error) *HookSet {
	if hs == nil || check == nil {
		return hs
	}
	checked := *hs
	checked.operationCheck = check
	return &checked
}

func (hs *HookSet) checkOperation(ctx context.Context) error {
	if hs == nil || hs.operationCheck == nil {
		return nil
	}
	return hs.operationCheck(ctx)
}

// Empty reports whether the HookSet has no hooks at any point.
func (hs *HookSet) Empty() bool {
	return hs == nil ||
		(len(hs.preAgentCall) == 0 && len(hs.postAgentCall) == 0 &&
			len(hs.preToolCall) == 0 && len(hs.postToolCall) == 0 &&
			len(hs.preLLMCall) == 0 && len(hs.postLLMCall) == 0 &&
			len(hs.preMemoryCall) == 0 && len(hs.postMemoryCall) == 0)
}

// RunPreAgentCall executes all PreAgentCall hooks in priority order.
// All hooks always run; errors are logged but never short-circuit.
func (hs *HookSet) RunPreAgentCall(ctx context.Context, hctx *PreAgentCallContext) {
	if hs == nil {
		return
	}
	for _, h := range hs.preAgentCall {
		if hs.checkOperation(ctx) != nil {
			return
		}
		h.OnPreAgentCall(ctx, hctx)
	}
}

// RunPostAgentCall executes all PostAgentCall hooks in priority order.
// All hooks always run; errors are logged but never short-circuit.
func (hs *HookSet) RunPostAgentCall(ctx context.Context, hctx *PostAgentCallContext) {
	if hs == nil {
		return
	}
	for _, h := range hs.postAgentCall {
		if hs.checkOperation(ctx) != nil {
			return
		}
		h.OnPostAgentCall(ctx, hctx)
	}
}

// RunPreToolCall executes all PreToolCall hooks in priority order.
// If any hook sets Block=true, the chain short-circuits.
// Rewritten Arguments are passed to subsequent hooks.
//
// Error policy: a failing hook is logged and skipped so that non-critical
// hooks never block tool execution. Security-critical
// checks must live in the core engine, not in disableable hook plugins.
func (hs *HookSet) RunPreToolCall(ctx context.Context, hctx *PreToolCallContext) (PreToolCallResult, error) {
	if hs == nil {
		return PreToolCallResult{}, nil
	}
	operationCtx := ctx
	var final PreToolCallResult
	for _, h := range hs.preToolCall {
		if err := hs.checkOperation(operationCtx); err != nil {
			return final, err
		}
		result, err := h.OnPreToolCall(ctx, hctx)
		if err != nil {
			slog.Warn("pre_tool_call hook error", "hook", h.Name(), "error", err)
			continue
		}
		if result.Arguments != nil {
			hctx.Arguments = result.Arguments
			final.Arguments = result.Arguments
		}
		if result.Context != nil {
			ctx = result.Context
			final.Context = result.Context
		}
		if result.Block {
			final.Block = true
			final.BlockMessage = result.BlockMessage
			return final, nil
		}
	}
	return final, nil
}

// RunPostToolCall executes all PostToolCall hooks in priority order.
// All hooks always run; errors are logged but never short-circuit.
func (hs *HookSet) RunPostToolCall(ctx context.Context, hctx *PostToolCallContext) {
	if hs == nil {
		return
	}
	for _, h := range hs.postToolCall {
		if hs.checkOperation(ctx) != nil {
			return
		}
		h.OnPostToolCall(ctx, hctx)
	}
}

// RunPreLLMCall executes all PreLLMCall hooks in priority order.
// Mutations from each hook are applied before the next hook runs.
//
// Error policy: same as RunPreToolCall — log and skip.
func (hs *HookSet) RunPreLLMCall(ctx context.Context, hctx *PreLLMCallContext) (PreLLMCallResult, error) {
	if hs == nil {
		return PreLLMCallResult{}, nil
	}
	operationCtx := ctx
	var final PreLLMCallResult
	for _, h := range hs.preLLMCall {
		if err := hs.checkOperation(operationCtx); err != nil {
			return final, err
		}
		result, err := h.OnPreLLMCall(ctx, hctx)
		if err != nil {
			slog.Warn("pre_llm_call hook error", "hook", h.Name(), "error", err)
			continue
		}
		if result.System != nil {
			hctx.System = *result.System
			final.System = result.System
		}
		if result.ToolDefinitions != nil {
			hctx.ToolDefinitions = result.ToolDefinitions
			final.ToolDefinitions = result.ToolDefinitions
		}
		if result.Model != nil {
			hctx.Model = *result.Model
			final.Model = result.Model
		}
		if result.Context != nil {
			ctx = result.Context
			final.Context = result.Context
		}
	}
	return final, nil
}

// RunPostLLMCall executes all PostLLMCall hooks in priority order.
// All hooks always run; errors are logged but never short-circuit.
func (hs *HookSet) RunPostLLMCall(ctx context.Context, hctx *PostLLMCallContext) {
	if hs == nil {
		return
	}
	for _, h := range hs.postLLMCall {
		if hs.checkOperation(ctx) != nil {
			return
		}
		h.OnPostLLMCall(ctx, hctx)
	}
}

// RunPreMemoryCall executes all PreMemoryCall hooks in priority order.
// Mutations from each hook are applied before the next hook runs.
//
// Error policy: same as RunPreToolCall — log and skip.
func (hs *HookSet) RunPreMemoryCall(ctx context.Context, hctx *PreMemoryCallContext) (PreMemoryCallResult, error) {
	if hs == nil {
		return PreMemoryCallResult{}, nil
	}
	operationCtx := ctx
	var final PreMemoryCallResult
	for _, h := range hs.preMemoryCall {
		if err := hs.checkOperation(operationCtx); err != nil {
			return final, err
		}
		result, err := h.OnPreMemoryCall(ctx, hctx)
		if err != nil {
			slog.Warn("pre_memory_call hook error", "hook", h.Name(), "error", err)
			continue
		}
		if result.Context != nil {
			ctx = result.Context
			final.Context = result.Context
		}
	}
	return final, nil
}

// RunPostMemoryCall executes all PostMemoryCall hooks in priority order.
// All hooks always run; errors are logged but never short-circuit.
func (hs *HookSet) RunPostMemoryCall(ctx context.Context, hctx *PostMemoryCallContext) {
	if hs == nil {
		return
	}
	for _, h := range hs.postMemoryCall {
		if hs.checkOperation(ctx) != nil {
			return
		}
		h.OnPostMemoryCall(ctx, hctx)
	}
}
