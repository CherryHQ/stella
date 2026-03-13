package tool

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
)

// fakeProvider returns a canned text response.
type fakeDelegateProvider struct {
	response string
}

func (f fakeDelegateProvider) API() string { return "fake" }

func (f fakeDelegateProvider) Stream(model ai.Model, ctx ai.Context, opts ai.StreamOptions) (ai.AssistantEventStream, error) {
	out := ai.NewChannelEventStream(8)
	go func() {
		out.Emit(ai.EventTextDelta{Text: f.response})
		out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
		out.Finish(nil)
	}()
	return out, nil
}

func (f fakeDelegateProvider) StreamSimple(model ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	return f.Stream(model, ctx, opts.StreamOptions)
}

func newTestDelegateTool(response string) *DelegateTool {
	reg := ai.NewRegistry()
	reg.Register(fakeDelegateProvider{response: response})

	toolReg := &Registry{tools: make(map[string]Tool)}
	toolReg.Register(&ReadTool{})
	toolReg.Register(&BashTool{workDir: "/tmp"})

	eng := &engine.Engine{Providers: reg}
	model := ai.Model{API: "fake", Name: "test-model"}

	dt := NewDelegateTool(DelegateConfig{
		Engine:   eng,
		Registry: toolReg,
		Model:    model,
		APIKey:   "test-key",
		System:   "You are a test assistant.",
	})
	toolReg.Register(dt)

	return dt
}

func TestDelegateSingleTask(t *testing.T) {
	dt := newTestDelegateTool("subagent result")

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":   "t1",
				"task": "Do something",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `"t1"`) {
		t.Fatalf("expected task ID t1 in result, got: %s", result)
	}
	if !strings.Contains(result, "subagent result") {
		t.Fatalf("expected 'subagent result' in output, got: %s", result)
	}
}

func TestDelegateParallelTasks(t *testing.T) {
	dt := newTestDelegateTool("parallel result")

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"id": "a", "task": "Task A"},
			map[string]any{"id": "b", "task": "Task B"},
			map[string]any{"id": "c", "task": "Task C"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if !strings.Contains(result, `"`+id+`"`) {
			t.Fatalf("expected task ID %s in result, got: %s", id, result)
		}
	}
}

func TestDelegateToolWhitelistValid(t *testing.T) {
	dt := newTestDelegateTool("whitelisted")

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":    "t1",
				"task":  "Read only",
				"tools": []any{"read"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "whitelisted") {
		t.Fatalf("expected output in result, got: %s", result)
	}
}

func TestDelegateToolWhitelistInvalid(t *testing.T) {
	dt := newTestDelegateTool("should not appear")

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":    "t1",
				"task":  "Use nonexistent tool",
				"tools": []any{"nonexistent_tool"},
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should contain an error for the task, not a panic.
	if !strings.Contains(result, "unknown tool") {
		t.Fatalf("expected 'unknown tool' error in result, got: %s", result)
	}
}

func TestDelegateDelegateExcludedFromChildren(t *testing.T) {
	dt := newTestDelegateTool("child response")

	toolSet, defs, err := dt.buildScopedTools(nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := toolSet[delegateToolName]; ok {
		t.Fatalf("delegate tool should be excluded from child tool set")
	}
	for _, def := range defs {
		if def.Name == delegateToolName {
			t.Fatalf("delegate tool should be excluded from child tool definitions")
		}
	}
}

func TestDelegateDelegateInWhitelistIgnored(t *testing.T) {
	dt := newTestDelegateTool("child response")

	// Requesting delegate in whitelist should silently skip it.
	toolSet, defs, err := dt.buildScopedTools([]string{"read", "delegate"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := toolSet[delegateToolName]; ok {
		t.Fatalf("delegate should be excluded even when explicitly whitelisted")
	}
	if len(defs) != 1 || defs[0].Name != "read" {
		t.Fatalf("expected only 'read' in defs, got: %v", defs)
	}
}

func TestDelegateEmptyWhitelistGivesNoTools(t *testing.T) {
	dt := newTestDelegateTool("no tools")

	// Explicit empty whitelist should yield zero tools.
	toolSet, defs, err := dt.buildScopedTools(nil, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(toolSet) != 0 {
		t.Fatalf("expected empty tool set, got %d tools", len(toolSet))
	}
	if len(defs) != 0 {
		t.Fatalf("expected empty defs, got %d", len(defs))
	}
}

func TestDelegateTimeout(t *testing.T) {
	// Use a provider that keeps looping with tool calls, paired with a slow tool.
	reg := ai.NewRegistry()
	reg.Register(loopingProvider{})

	toolReg := &Registry{tools: make(map[string]Tool)}
	toolReg.Register(&slowTool{})
	eng := &engine.Engine{Providers: reg}

	dt := NewDelegateTool(DelegateConfig{
		Engine:   eng,
		Registry: toolReg,
		Model:    ai.Model{API: "looping", Name: "stub"},
		APIKey:   "test",
		System:   "test",
	})
	toolReg.Register(dt)

	start := time.Now()
	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":              "slow",
				"task":            "Do slow thing",
				"timeout_seconds": float64(1),
			},
		},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should complete around 1 second via context timeout, not run all turns.
	if elapsed > 5*time.Second {
		t.Fatalf("expected timeout around 1s, took %v", elapsed)
	}
	// Result should contain an error (context deadline exceeded).
	if !strings.Contains(result, "error") {
		t.Logf("result: %s", result)
	}
}

// loopingProvider always emits a tool call to keep the loop going.
type loopingProvider struct{}

func (loopingProvider) API() string { return "looping" }

func (loopingProvider) Stream(_ ai.Model, _ ai.Context, _ ai.StreamOptions) (ai.AssistantEventStream, error) {
	out := ai.NewChannelEventStream(8)
	go func() {
		out.Emit(ai.EventToolCallDelta{ID: "call_1", Name: "slow_tool", Arguments: "{}"})
		out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
		out.Finish(nil)
	}()
	return out, nil
}

func (loopingProvider) StreamSimple(_ ai.Model, _ ai.Context, _ ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	return loopingProvider{}.Stream(ai.Model{}, ai.Context{}, ai.StreamOptions{})
}

// slowTool sleeps until context is cancelled.
type slowTool struct{}

func (s *slowTool) Definition() toolspec.Definition {
	return toolspec.Definition{
		Name:        "slow_tool",
		Description: "A tool that blocks.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}
}

func (s *slowTool) Execute(ctx context.Context, _ map[string]any) (string, error) {
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(30 * time.Second):
		return "done", nil
	}
}

func TestDelegateOutputTruncation(t *testing.T) {
	long := strings.Repeat("x", maxResultChars+100)
	truncated := truncateResult(long, maxResultChars)

	if !strings.HasSuffix(truncated, "[truncated]") {
		t.Fatalf("expected [truncated] suffix")
	}
	// Should be maxResultChars runes + "\n[truncated]"
	runes := []rune(truncated)
	expected := maxResultChars + len([]rune("\n[truncated]"))
	if len(runes) != expected {
		t.Fatalf("expected %d runes, got %d", expected, len(runes))
	}
}

func TestDelegateParseErrors(t *testing.T) {
	dt := newTestDelegateTool("unused")

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing tasks", map[string]any{}, "tasks is required"},
		{"tasks not array", map[string]any{"tasks": "bad"}, "tasks must be an array"},
		{"missing id", map[string]any{"tasks": []any{map[string]any{"task": "x"}}}, "id is required"},
		{"missing task", map[string]any{"tasks": []any{map[string]any{"id": "x"}}}, "task is required"},
		{"duplicate id", map[string]any{"tasks": []any{
			map[string]any{"id": "x", "task": "a"},
			map[string]any{"id": "x", "task": "b"},
		}}, "duplicate id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := dt.Execute(context.Background(), tt.args)
			if err == nil {
				t.Fatalf("expected error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got: %v", tt.want, err)
			}
		})
	}
}

func TestDelegatePanicRecovery(t *testing.T) {
	// Create a provider that panics.
	reg := ai.NewRegistry()
	reg.Register(panicProvider{})

	toolReg := &Registry{tools: make(map[string]Tool)}
	eng := &engine.Engine{Providers: reg}

	dt := NewDelegateTool(DelegateConfig{
		Engine:   eng,
		Registry: toolReg,
		Model:    ai.Model{API: "panic", Name: "stub"},
		APIKey:   "test",
		System:   "test",
	})

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"id": "boom", "task": "Cause panic"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "panic") || !strings.Contains(result, "boom") {
		// The result should contain the task ID and a panic error.
		t.Logf("result: %s", result)
	}
}

type panicProvider struct{}

func (panicProvider) API() string { return "panic" }

func (panicProvider) Stream(ai.Model, ai.Context, ai.StreamOptions) (ai.AssistantEventStream, error) {
	panic("test panic in provider")
}

func (panicProvider) StreamSimple(ai.Model, ai.Context, ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	panic("test panic in provider")
}

func TestDelegateDefinition(t *testing.T) {
	dt := newTestDelegateTool("unused")
	def := dt.Definition()

	if def.Name != "delegate" {
		t.Fatalf("expected name 'delegate', got %q", def.Name)
	}
	if def.InputSchema == nil {
		t.Fatalf("expected non-nil input schema")
	}

	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties in schema")
	}
	if _, ok := props["tasks"]; !ok {
		t.Fatalf("expected 'tasks' property in schema")
	}
}

func TestExtractLastAssistantText(t *testing.T) {
	tests := []struct {
		name    string
		history []ai.Message
		want    string
	}{
		{
			"empty history",
			nil,
			"",
		},
		{
			"single assistant",
			[]ai.Message{
				ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "hello"}}},
			},
			"hello",
		},
		{
			"user then assistant",
			[]ai.Message{
				ai.UserMessage{Content: "hi"},
				ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "response"}}},
			},
			"response",
		},
		{
			"last assistant wins",
			[]ai.Message{
				ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "first"}}},
				ai.UserMessage{Content: "more"},
				ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "second"}}},
			},
			"second",
		},
		{
			"tool result after assistant",
			[]ai.Message{
				ai.AssistantMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "before tool"}}},
				ai.ToolResultMessage{Content: []ai.ContentBlock{ai.TextContent{Text: "tool output"}}},
			},
			"before tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractLastAssistantText(tt.history)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}

func TestRegistryHas(t *testing.T) {
	r := &Registry{tools: make(map[string]Tool)}
	r.Register(&ReadTool{})

	if !r.Has("read") {
		t.Fatalf("expected Has('read') to be true")
	}
	if r.Has("nonexistent") {
		t.Fatalf("expected Has('nonexistent') to be false")
	}
}

// Verify that the toolspec.Definition interface is satisfied.
var _ Tool = (*DelegateTool)(nil)

// Suppress unused import warnings for toolspec.
var _ toolspec.Definition
