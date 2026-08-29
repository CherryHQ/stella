package hooks

import (
	"context"
	"testing"
	"time"

	"github.com/CherryHQ/stella/pkg/ai"
)

// --- test helpers ---

type mockPlugin struct {
	name     string
	priority int
}

func (m *mockPlugin) Name() string  { return m.name }
func (m *mockPlugin) Priority() int { return m.priority }

// preToolCallPlugin records calls and optionally rewrites/blocks.
type preToolCallPlugin struct {
	mockPlugin
	called   bool
	rewrite  map[string]any
	block    bool
	blockMsg string
}

func (p *preToolCallPlugin) OnPreToolCall(_ context.Context, hctx *PreToolCallContext) (PreToolCallResult, error) {
	p.called = true
	return PreToolCallResult{
		Arguments:    p.rewrite,
		Block:        p.block,
		BlockMessage: p.blockMsg,
	}, nil
}

// postToolCallPlugin records calls.
type postToolCallPlugin struct {
	mockPlugin
	called   bool
	toolName string
}

func (p *postToolCallPlugin) OnPostToolCall(_ context.Context, hctx *PostToolCallContext) {
	p.called = true
	p.toolName = hctx.ToolName
}

// preLLMCallPlugin optionally overrides system prompt.
type preLLMCallPlugin struct {
	mockPlugin
	called    bool
	newSystem *string
}

func (p *preLLMCallPlugin) OnPreLLMCall(_ context.Context, _ *PreLLMCallContext) (PreLLMCallResult, error) {
	p.called = true
	return PreLLMCallResult{System: p.newSystem}, nil
}

type preToolContextPlugin struct {
	mockPlugin
	ctx   context.Context
	check func(context.Context)
}

func (p *preToolContextPlugin) OnPreToolCall(ctx context.Context, _ *PreToolCallContext) (PreToolCallResult, error) {
	if p.check != nil {
		p.check(ctx)
	}
	return PreToolCallResult{Context: p.ctx}, nil
}

type preLLMContextPlugin struct {
	mockPlugin
	ctx   context.Context
	check func(context.Context)
}

func (p *preLLMContextPlugin) OnPreLLMCall(ctx context.Context, _ *PreLLMCallContext) (PreLLMCallResult, error) {
	if p.check != nil {
		p.check(ctx)
	}
	return PreLLMCallResult{Context: p.ctx}, nil
}

// postLLMCallPlugin records usage.
type postLLMCallPlugin struct {
	mockPlugin
	called   bool
	duration time.Duration
}

func (p *postLLMCallPlugin) OnPostLLMCall(_ context.Context, hctx *PostLLMCallContext) {
	p.called = true
	p.duration = hctx.Duration
}

// --- tests ---

func TestNewHookSet_SortsByPriorityThenName(t *testing.T) {
	a := &preToolCallPlugin{mockPlugin: mockPlugin{name: "beta", priority: 10}}
	b := &preToolCallPlugin{mockPlugin: mockPlugin{name: "alpha", priority: 10}}
	c := &preToolCallPlugin{mockPlugin: mockPlugin{name: "gamma", priority: 5}}

	hs := NewHookSet([]HookPlugin{a, b, c})

	// Expected order: gamma(5), alpha(10), beta(10)
	if len(hs.preToolCall) != 3 {
		t.Fatalf("expected 3 pre_tool_call hooks, got %d", len(hs.preToolCall))
	}
	if hs.preToolCall[0].Name() != "gamma" {
		t.Errorf("expected first hook gamma, got %s", hs.preToolCall[0].Name())
	}
	if hs.preToolCall[1].Name() != "alpha" {
		t.Errorf("expected second hook alpha, got %s", hs.preToolCall[1].Name())
	}
	if hs.preToolCall[2].Name() != "beta" {
		t.Errorf("expected third hook beta, got %s", hs.preToolCall[2].Name())
	}
}

func TestHookSet_Empty(t *testing.T) {
	var nilSet *HookSet
	if !nilSet.Empty() {
		t.Error("nil HookSet should be empty")
	}

	empty := NewHookSet(nil)
	if !empty.Empty() {
		t.Error("HookSet with no plugins should be empty")
	}

	nonEmpty := NewHookSet([]HookPlugin{
		&preToolCallPlugin{mockPlugin: mockPlugin{name: "a", priority: 0}},
	})
	if nonEmpty.Empty() {
		t.Error("HookSet with plugins should not be empty")
	}
}

func TestRunPreToolCall_RewriteChaining(t *testing.T) {
	h1 := &preToolCallPlugin{
		mockPlugin: mockPlugin{name: "first", priority: 1},
		rewrite:    map[string]any{"command": "rewritten-by-first"},
	}
	h2 := &preToolCallPlugin{
		mockPlugin: mockPlugin{name: "second", priority: 2},
	}

	hs := NewHookSet([]HookPlugin{h1, h2})
	hctx := &PreToolCallContext{
		ToolName:  "bash",
		Arguments: map[string]any{"command": "original"},
	}

	result := hs.RunPreToolCall(context.Background(), hctx)
	if !h1.called || !h2.called {
		t.Error("both hooks should have been called")
	}
	// h2 should see the rewritten args from h1
	if hctx.Arguments["command"] != "rewritten-by-first" {
		t.Errorf("expected rewritten args, got %v", hctx.Arguments["command"])
	}
	if result.Arguments["command"] != "rewritten-by-first" {
		t.Errorf("expected result to carry rewritten args")
	}
}

type contextKey string

func TestRunPreToolCall_ContextChaining(t *testing.T) {
	key := contextKey("trace")
	first := &preToolContextPlugin{
		mockPlugin: mockPlugin{name: "first", priority: 1},
		ctx:        context.WithValue(context.Background(), key, "parent"),
	}
	second := &preToolContextPlugin{
		mockPlugin: mockPlugin{name: "second", priority: 2},
		check: func(ctx context.Context) {
			if got := ctx.Value(key); got != "parent" {
				t.Fatalf("second hook saw context value %#v, want parent", got)
			}
		},
	}

	result := NewHookSet([]HookPlugin{first, second}).RunPreToolCall(context.Background(), &PreToolCallContext{})
	if result.Context == nil || result.Context.Value(key) != "parent" {
		t.Fatalf("result context did not preserve chained value")
	}
}

func TestRunPreToolCall_BlockShortCircuits(t *testing.T) {
	h1 := &preToolCallPlugin{
		mockPlugin: mockPlugin{name: "blocker", priority: 1},
		block:      true,
		blockMsg:   "blocked by policy",
	}
	h2 := &preToolCallPlugin{
		mockPlugin: mockPlugin{name: "after", priority: 2},
	}

	hs := NewHookSet([]HookPlugin{h1, h2})
	hctx := &PreToolCallContext{ToolName: "bash", Arguments: map[string]any{}}

	result := hs.RunPreToolCall(context.Background(), hctx)
	if !result.Block {
		t.Error("expected block=true")
	}
	if result.BlockMessage != "blocked by policy" {
		t.Errorf("expected block message, got %q", result.BlockMessage)
	}
	if h2.called {
		t.Error("hooks after blocker should not run")
	}
}

func TestRunPostToolCall_AlwaysRunsAll(t *testing.T) {
	h1 := &postToolCallPlugin{mockPlugin: mockPlugin{name: "a", priority: 1}}
	h2 := &postToolCallPlugin{mockPlugin: mockPlugin{name: "b", priority: 2}}

	hs := NewHookSet([]HookPlugin{h1, h2})
	hctx := &PostToolCallContext{
		ToolName: "read",
		Result:   "file contents",
		Duration: 50 * time.Millisecond,
	}

	hs.RunPostToolCall(context.Background(), hctx)
	if !h1.called || !h2.called {
		t.Error("all post hooks should run")
	}
	if h1.toolName != "read" {
		t.Errorf("expected tool name 'read', got %q", h1.toolName)
	}
}

func TestRunPreLLMCall_ContextChaining(t *testing.T) {
	key := contextKey("llm")
	first := &preLLMContextPlugin{
		mockPlugin: mockPlugin{name: "first", priority: 1},
		ctx:        context.WithValue(context.Background(), key, "span"),
	}
	second := &preLLMContextPlugin{
		mockPlugin: mockPlugin{name: "second", priority: 2},
		check: func(ctx context.Context) {
			if got := ctx.Value(key); got != "span" {
				t.Fatalf("second hook saw context value %#v, want span", got)
			}
		},
	}

	result := NewHookSet([]HookPlugin{first, second}).RunPreLLMCall(context.Background(), &PreLLMCallContext{})
	if result.Context == nil || result.Context.Value(key) != "span" {
		t.Fatalf("result context did not preserve chained value")
	}
}

func TestRunPreLLMCall_SystemOverride(t *testing.T) {
	newSys := "modified system prompt"
	h := &preLLMCallPlugin{
		mockPlugin: mockPlugin{name: "sys", priority: 1},
		newSystem:  &newSys,
	}

	hs := NewHookSet([]HookPlugin{h})
	hctx := &PreLLMCallContext{
		Model:  "claude-sonnet",
		System: "original",
	}

	result := hs.RunPreLLMCall(context.Background(), hctx)
	if !h.called {
		t.Error("hook should be called")
	}
	if result.System == nil || *result.System != newSys {
		t.Errorf("expected system override")
	}
	if hctx.System != newSys {
		t.Errorf("expected context to be updated, got %q", hctx.System)
	}
}

func TestRunPostLLMCall_Telemetry(t *testing.T) {
	h := &postLLMCallPlugin{mockPlugin: mockPlugin{name: "tel", priority: 1}}

	hs := NewHookSet([]HookPlugin{h})
	hctx := &PostLLMCallContext{
		Model:    "claude-sonnet",
		Usage:    ai.Usage{InputTokens: 100, OutputTokens: 50},
		Duration: 2 * time.Second,
	}

	hs.RunPostLLMCall(context.Background(), hctx)
	if !h.called {
		t.Error("hook should be called")
	}
	if h.duration != 2*time.Second {
		t.Errorf("expected 2s duration, got %v", h.duration)
	}
}

func TestNilHookSet_NoOp(t *testing.T) {
	var hs *HookSet

	result := hs.RunPreToolCall(context.Background(), &PreToolCallContext{})
	if result.Block {
		t.Error("nil set should be no-op")
	}

	hs.RunPostToolCall(context.Background(), &PostToolCallContext{})

	llmResult := hs.RunPreLLMCall(context.Background(), &PreLLMCallContext{})
	if llmResult.System != nil {
		t.Error("nil set should be no-op")
	}

	hs.RunPostLLMCall(context.Background(), &PostLLMCallContext{})
}

func TestMultiPointPlugin_SeparateSlices(t *testing.T) {
	// Verify that the HookSet correctly categorizes plugins into separate point slices.
	preOnly := &preToolCallPlugin{mockPlugin: mockPlugin{name: "pre", priority: 1}}
	postOnly := &postToolCallPlugin{mockPlugin: mockPlugin{name: "post", priority: 2}}

	hs := NewHookSet([]HookPlugin{preOnly, postOnly})
	if len(hs.preToolCall) != 1 {
		t.Errorf("expected 1 pre hook, got %d", len(hs.preToolCall))
	}
	if len(hs.postToolCall) != 1 {
		t.Errorf("expected 1 post hook, got %d", len(hs.postToolCall))
	}
}

// --- additional agent/memory hook test helpers ---

type preAgentPlugin struct {
	mockPlugin
	called  bool
	channel string
}

func (p *preAgentPlugin) OnPreAgentCall(_ context.Context, hctx *PreAgentCallContext) {
	p.called = true
	p.channel = hctx.Channel
}

type postAgentPlugin struct {
	mockPlugin
	called bool
	dur    time.Duration
}

func (p *postAgentPlugin) OnPostAgentCall(_ context.Context, hctx *PostAgentCallContext) {
	p.called = true
	p.dur = hctx.Duration
}

type preMemoryPlugin struct {
	mockPlugin
	called bool
	op     MemoryOp
	ctx    context.Context
}

func (p *preMemoryPlugin) OnPreMemoryCall(_ context.Context, hctx *PreMemoryCallContext) (PreMemoryCallResult, error) {
	p.called = true
	p.op = hctx.Op
	return PreMemoryCallResult{Context: p.ctx}, nil
}

type postMemoryPlugin struct {
	mockPlugin
	called bool
	op     MemoryOp
}

func (p *postMemoryPlugin) OnPostMemoryCall(_ context.Context, hctx *PostMemoryCallContext) {
	p.called = true
	p.op = hctx.Op
}

func TestRunPreAgentCall(t *testing.T) {
	h := &preAgentPlugin{mockPlugin: mockPlugin{name: "agent-pre", priority: 1}}
	hs := NewHookSet([]HookPlugin{h})

	hctx := &PreAgentCallContext{Channel: "telegram", MessageLen: 42}
	hs.RunPreAgentCall(context.Background(), hctx)

	if !h.called {
		t.Error("expected hook to be called")
	}
	if h.channel != "telegram" {
		t.Errorf("expected channel 'telegram', got %q", h.channel)
	}
}

func TestRunPreAgentCall_Nil(t *testing.T) {
	var hs *HookSet
	// Should not panic.
	hs.RunPreAgentCall(context.Background(), &PreAgentCallContext{})
}

func TestRunPostAgentCall(t *testing.T) {
	h := &postAgentPlugin{mockPlugin: mockPlugin{name: "agent-post", priority: 1}}
	hs := NewHookSet([]HookPlugin{h})

	hctx := &PostAgentCallContext{Duration: 3 * time.Second}
	hs.RunPostAgentCall(context.Background(), hctx)

	if !h.called {
		t.Error("expected hook to be called")
	}
	if h.dur != 3*time.Second {
		t.Errorf("expected 3s duration, got %v", h.dur)
	}
}

func TestRunPostAgentCall_Nil(t *testing.T) {
	var hs *HookSet
	hs.RunPostAgentCall(context.Background(), &PostAgentCallContext{})
}

func TestRunPreMemoryCall(t *testing.T) {
	key := contextKey("memory")
	h := &preMemoryPlugin{
		mockPlugin: mockPlugin{name: "memory-pre", priority: 1},
		ctx:        context.WithValue(context.Background(), key, "span"),
	}
	hs := NewHookSet([]HookPlugin{h})

	result := hs.RunPreMemoryCall(context.Background(), &PreMemoryCallContext{Op: MemoryOpAppend})
	if !h.called {
		t.Error("expected hook to be called")
	}
	if h.op != MemoryOpAppend {
		t.Errorf("expected op MemoryOpAppend, got %q", h.op)
	}
	if result.Context == nil || result.Context.Value(key) != "span" {
		t.Fatal("expected enriched context")
	}
}

func TestRunPreMemoryCall_Nil(t *testing.T) {
	var hs *HookSet
	result := hs.RunPreMemoryCall(context.Background(), &PreMemoryCallContext{})
	if result.Context != nil {
		t.Fatal("nil hook set should be no-op")
	}
}

func TestRunPostMemoryCall(t *testing.T) {
	h := &postMemoryPlugin{mockPlugin: mockPlugin{name: "memory-post", priority: 1}}
	hs := NewHookSet([]HookPlugin{h})

	hctx := &PostMemoryCallContext{Op: MemoryOpAppend}
	hs.RunPostMemoryCall(context.Background(), hctx)

	if !h.called {
		t.Error("expected hook to be called")
	}
	if h.op != MemoryOpAppend {
		t.Errorf("expected op MemoryOpAppend, got %q", h.op)
	}
}

func TestRunPostMemoryCall_Nil(t *testing.T) {
	var hs *HookSet
	hs.RunPostMemoryCall(context.Background(), &PostMemoryCallContext{})
}
