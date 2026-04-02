package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/agent/engine"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/toolspec"
)

// fakeAgentProvider returns a canned text response.
type fakeAgentProvider struct {
	response string
}

func (f fakeAgentProvider) API() string { return "fake" }

func (f fakeAgentProvider) Stream(_ ai.Model, _ ai.Context, _ ai.StreamOptions) (ai.AssistantEventStream, error) {
	out := ai.NewChannelEventStream(8)
	go func() {
		out.Emit(ai.EventTextDelta{Text: f.response})
		out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
		out.Finish(nil)
	}()
	return out, nil
}

func (f fakeAgentProvider) StreamSimple(_ ai.Model, _ ai.Context, opts ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	return f.Stream(ai.Model{}, ai.Context{}, opts.StreamOptions)
}

func newTestAgentTool(response string) *AgentTool {
	return newTestAgentToolWithConfig(response, AgentConfig{})
}

func newTestAgentToolWithConfig(response string, overrides AgentConfig) *AgentTool {
	reg := ai.NewRegistry()
	reg.Register(fakeAgentProvider{response: response})

	toolReg := &Registry{tools: make(map[string]Tool)}
	toolReg.Register(&ReadTool{})
	toolReg.Register(&BashTool{workDir: "/tmp"})

	eng := &engine.Engine{Providers: reg}
	model := ai.Model{API: "fake", Name: "test-model"}

	// Build test presets if not overridden.
	presets := overrides.Presets
	if presets == nil {
		presets = NewPresetRegistry([]AgentPreset{
			{
				Name:        "reviewer",
				Description: "Test reviewer preset",
				System:      "Review code carefully.",
				Tools:       []string{"read", "bash"},
				HasTools:    true,
				MaxTurns:    10,
				Timeout:     2 * time.Minute,
			},
			{
				Name:        "writer",
				Description: "Test writer preset",
				System:      "Write clearly.",
				Tools:       []string{},
				HasTools:    true,
				MaxTurns:    5,
				Timeout:     1 * time.Minute,
			},
		})
	}

	cfg := AgentConfig{
		Engine:         eng,
		Registry:       toolReg,
		Model:          model,
		APIKey:         "test-key",
		System:         "You are a test assistant.",
		Presets:        presets,
		MaxTasks:       overrides.MaxTasks,
		MaxConcurrency: overrides.MaxConcurrency,
		MaxResultChars: overrides.MaxResultChars,
		Emit:           overrides.Emit,
	}

	dt := NewAgentTool(cfg)
	toolReg.Register(dt)

	return dt
}

func TestAgentSingleTask(t *testing.T) {
	dt := newTestAgentTool("subagent result")

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
	if !strings.Contains(result, `"complete":true`) {
		t.Fatalf("expected complete:true in result, got: %s", result)
	}
}

func TestAgentParallelTasks(t *testing.T) {
	dt := newTestAgentTool("parallel result")

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

func TestAgentToolWhitelistValid(t *testing.T) {
	dt := newTestAgentTool("whitelisted")

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

func TestAgentToolWhitelistInvalid(t *testing.T) {
	dt := newTestAgentTool("should not appear")

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
	if !strings.Contains(result, "unknown tool") {
		t.Fatalf("expected 'unknown tool' error in result, got: %s", result)
	}
}

func TestAgentExcludedFromChildren(t *testing.T) {
	dt := newTestAgentTool("child response")

	toolSet, defs, err := dt.buildScopedTools(nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := toolSet[agentToolName]; ok {
		t.Fatalf("agent tool should be excluded from child tool set")
	}
	for _, def := range defs {
		if def.Name == agentToolName {
			t.Fatalf("agent tool should be excluded from child tool definitions")
		}
	}
}

func TestAgentInWhitelistIgnored(t *testing.T) {
	dt := newTestAgentTool("child response")

	// Requesting agent in whitelist should silently skip it.
	toolSet, defs, err := dt.buildScopedTools([]string{"read", "agent"}, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := toolSet[agentToolName]; ok {
		t.Fatalf("agent should be excluded even when explicitly whitelisted")
	}
	if len(defs) != 1 || defs[0].Name != "read" {
		t.Fatalf("expected only 'read' in defs, got: %v", defs)
	}
}

func TestAgentEmptyWhitelistGivesNoTools(t *testing.T) {
	dt := newTestAgentTool("no tools")

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

func TestAgentTimeout(t *testing.T) {
	reg := ai.NewRegistry()
	reg.Register(loopingProvider{})

	toolReg := &Registry{tools: make(map[string]Tool)}
	toolReg.Register(&slowTool{})
	eng := &engine.Engine{Providers: reg}

	dt := NewAgentTool(AgentConfig{
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
	if elapsed > 5*time.Second {
		t.Fatalf("expected timeout around 1s, took %v", elapsed)
	}
	if !strings.Contains(result, "error") {
		t.Fatalf("expected 'error' in result, got: %s", result)
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

func TestAgentOutputTruncation(t *testing.T) {
	long := strings.Repeat("x", defaultMaxResultChars+100)
	truncated := truncateResult(long, defaultMaxResultChars)

	if !strings.HasSuffix(truncated, "[truncated]") {
		t.Fatalf("expected [truncated] suffix")
	}
	runes := []rune(truncated)
	expected := defaultMaxResultChars + len([]rune("\n[truncated]"))
	if len(runes) != expected {
		t.Fatalf("expected %d runes, got %d", expected, len(runes))
	}
}

func TestAgentParseErrors(t *testing.T) {
	dt := newTestAgentTool("unused")

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

func TestAgentPanicRecovery(t *testing.T) {
	reg := ai.NewRegistry()
	reg.Register(panicProvider{})

	toolReg := &Registry{tools: make(map[string]Tool)}
	eng := &engine.Engine{Providers: reg}

	dt := NewAgentTool(AgentConfig{
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
	if !strings.Contains(result, "panic") {
		t.Fatalf("expected 'panic' in result, got: %s", result)
	}
	if !strings.Contains(result, "boom") {
		t.Fatalf("expected task ID 'boom' in result, got: %s", result)
	}
	// Should include stack trace.
	if !strings.Contains(result, "goroutine") {
		t.Fatalf("expected stack trace in panic result, got: %s", result)
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

func TestAgentDefinition(t *testing.T) {
	dt := newTestAgentTool("unused")
	def := dt.Definition()

	if def.Name != "agent" {
		t.Fatalf("expected name 'agent', got %q", def.Name)
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

	// Verify preset and context fields exist in task items.
	tasks := props["tasks"].(map[string]any)
	items := tasks["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	if _, ok := itemProps["preset"]; !ok {
		t.Fatalf("expected 'preset' property in task items")
	}
	if _, ok := itemProps["context"]; !ok {
		t.Fatalf("expected 'context' property in task items")
	}
}

func TestExtractLastAssistant(t *testing.T) {
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
			got, _ := extractLastAssistant(tt.history)
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

// --- Preset tests ---

func TestAgentPresetApplied(t *testing.T) {
	dt := newTestAgentTool("preset result")

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":     "t1",
				"task":   "Review my code",
				"preset": "reviewer",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "preset result") {
		t.Fatalf("expected output in result, got: %s", result)
	}
}

func TestAgentPresetUnknown(t *testing.T) {
	dt := newTestAgentTool("unused")

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":     "t1",
				"task":   "Do something",
				"preset": "nonexistent_preset",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "unknown preset") {
		t.Fatalf("expected 'unknown preset' error in result, got: %s", result)
	}
}

func TestAgentPresetOverriddenByExplicit(t *testing.T) {
	tc := agentTaskConfig{
		MaxTurns: 25, // explicit override
	}
	p := AgentPreset{
		MaxTurns: 10,
		System:   "preset system",
		Timeout:  3 * time.Minute,
	}
	tc.applyPreset(p)

	if tc.MaxTurns != 25 {
		t.Fatalf("expected explicit MaxTurns=25, got %d", tc.MaxTurns)
	}
	if tc.System != "preset system" {
		t.Fatalf("expected preset system, got %q", tc.System)
	}
	if tc.TimeoutSeconds != 180 {
		t.Fatalf("expected timeout 180s from preset, got %d", tc.TimeoutSeconds)
	}
}

func TestPresetNames(t *testing.T) {
	presets := NewPresetRegistry([]AgentPreset{
		{Name: "a", Description: "test a"},
		{Name: "b", Description: "test b"},
	})
	names := presets.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 preset names, got %d", len(names))
	}
	for _, name := range names {
		if _, ok := presets.Lookup(name); !ok {
			t.Fatalf("preset %q not found via Lookup", name)
		}
	}
}

// --- Context passing tests ---

func TestAgentContextPrepended(t *testing.T) {
	// Use a provider that echoes back the user message.
	reg := ai.NewRegistry()
	reg.Register(echoProvider{})

	toolReg := &Registry{tools: make(map[string]Tool)}
	eng := &engine.Engine{Providers: reg}

	dt := NewAgentTool(AgentConfig{
		Engine:   eng,
		Registry: toolReg,
		Model:    ai.Model{API: "echo", Name: "test"},
		APIKey:   "test",
		System:   "test",
	})

	result, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{
				"id":      "t1",
				"task":    "Summarize the file",
				"context": "File content: hello world",
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The echo provider returns what it receives. Check context was included.
	if !strings.Contains(result, "File content: hello world") {
		t.Fatalf("expected context in result, got: %s", result)
	}
	if !strings.Contains(result, "Summarize the file") {
		t.Fatalf("expected task in result, got: %s", result)
	}
}

// echoProvider returns the user message content as the assistant response.
type echoProvider struct{}

func (echoProvider) API() string { return "echo" }

func (echoProvider) Stream(_ ai.Model, ctx ai.Context, _ ai.StreamOptions) (ai.AssistantEventStream, error) {
	out := ai.NewChannelEventStream(8)
	var userText string
	for _, msg := range ctx.Messages {
		if um, ok := msg.(ai.UserMessage); ok {
			if s, ok := um.Content.(string); ok {
				userText = s
			}
		}
	}
	go func() {
		out.Emit(ai.EventTextDelta{Text: userText})
		out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
		out.Finish(nil)
	}()
	return out, nil
}

func (echoProvider) StreamSimple(_ ai.Model, ctx ai.Context, opts ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	return echoProvider{}.Stream(ai.Model{}, ctx, opts.StreamOptions)
}

// --- Concurrency & limits tests ---

func TestAgentMaxTasksExceeded(t *testing.T) {
	dt := newTestAgentToolWithConfig("unused", AgentConfig{MaxTasks: 2})

	_, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"id": "a", "task": "A"},
			map[string]any{"id": "b", "task": "B"},
			map[string]any{"id": "c", "task": "C"},
		},
	})
	if err == nil {
		t.Fatalf("expected error for exceeding max tasks")
	}
	if !strings.Contains(err.Error(), "too many tasks") {
		t.Fatalf("expected 'too many tasks' error, got: %v", err)
	}
}

func TestAgentConcurrencyLimited(t *testing.T) {
	// Track max concurrent executions.
	var mu sync.Mutex
	var current, maxSeen int

	reg := ai.NewRegistry()
	reg.Register(countingProvider{
		mu:      &mu,
		current: &current,
		maxSeen: &maxSeen,
	})

	toolReg := &Registry{tools: make(map[string]Tool)}
	eng := &engine.Engine{Providers: reg}

	dt := NewAgentTool(AgentConfig{
		Engine:         eng,
		Registry:       toolReg,
		Model:          ai.Model{API: "counting", Name: "test"},
		APIKey:         "test",
		System:         "test",
		MaxConcurrency: 2,
		MaxTasks:       10,
	})

	tasks := make([]any, 5)
	for i := range tasks {
		tasks[i] = map[string]any{
			"id":   fmt.Sprintf("t%d", i),
			"task": "Do something",
		}
	}

	_, err := dt.Execute(context.Background(), map[string]any{"tasks": tasks})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if maxSeen > 2 {
		t.Fatalf("expected max concurrency 2, saw %d", maxSeen)
	}
}

// countingProvider tracks concurrent invocations.
type countingProvider struct {
	mu      *sync.Mutex
	current *int
	maxSeen *int
}

func (c countingProvider) API() string { return "counting" }

func (c countingProvider) Stream(_ ai.Model, _ ai.Context, _ ai.StreamOptions) (ai.AssistantEventStream, error) {
	c.mu.Lock()
	*c.current++
	if *c.current > *c.maxSeen {
		*c.maxSeen = *c.current
	}
	c.mu.Unlock()

	// Small sleep to allow concurrency overlap.
	time.Sleep(50 * time.Millisecond)

	c.mu.Lock()
	*c.current--
	c.mu.Unlock()

	out := ai.NewChannelEventStream(8)
	go func() {
		out.Emit(ai.EventTextDelta{Text: "done"})
		out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
		out.Finish(nil)
	}()
	return out, nil
}

func (c countingProvider) StreamSimple(_ ai.Model, _ ai.Context, opts ai.SimpleStreamOptions) (ai.AssistantEventStream, error) {
	return c.Stream(ai.Model{}, ai.Context{}, opts.StreamOptions)
}

// --- Event emission tests ---

func TestAgentEmitsEvents(t *testing.T) {
	var events []engine.LoopEvent
	var mu sync.Mutex

	dt := newTestAgentToolWithConfig("event result", AgentConfig{
		Emit: func(ev engine.LoopEvent) {
			mu.Lock()
			events = append(events, ev)
			mu.Unlock()
		},
	})

	_, err := dt.Execute(context.Background(), map[string]any{
		"tasks": []any{
			map[string]any{"id": "t1", "task": "Do something"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(events) != 2 {
		t.Fatalf("expected 2 events (started+finished), got %d", len(events))
	}
	if events[0].Kind() != "subAgentStarted" {
		t.Fatalf("expected subAgentStarted, got %s", events[0].Kind())
	}
	if events[1].Kind() != "subAgentFinished" {
		t.Fatalf("expected subAgentFinished, got %s", events[1].Kind())
	}
}

// Verify that the Tool interface is satisfied.
var _ Tool = (*AgentTool)(nil)

// Suppress unused import warnings for toolspec.
var _ toolspec.Definition

// --- Preset loading tests ---

func TestLoadPresetFromMarkdown(t *testing.T) {
	dir := t.TempDir()

	// Write a test preset file.
	content := `---
name: test-agent
description: A test agent preset.
tools: [read, bash]
max_turns: 15
timeout: 3m
---
You are a test agent. Be helpful and concise.
`
	if err := os.WriteFile(filepath.Join(dir, "test-agent.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	presets := loadPresetsFromDir(dir, "test")
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}

	p := presets[0]
	if p.Name != "test-agent" {
		t.Fatalf("expected name 'test-agent', got %q", p.Name)
	}
	if p.Description != "A test agent preset." {
		t.Fatalf("expected description, got %q", p.Description)
	}
	if !strings.Contains(p.System, "test agent") {
		t.Fatalf("expected system prompt from body, got %q", p.System)
	}
	if !p.HasTools || len(p.Tools) != 2 {
		t.Fatalf("expected 2 tools with HasTools=true, got %v (HasTools=%v)", p.Tools, p.HasTools)
	}
	if p.MaxTurns != 15 {
		t.Fatalf("expected max_turns=15, got %d", p.MaxTurns)
	}
	if p.Timeout != 3*time.Minute {
		t.Fatalf("expected timeout=3m, got %v", p.Timeout)
	}
	if p.Source != "test" {
		t.Fatalf("expected source 'test', got %q", p.Source)
	}
}

func TestLoadPresetEmptyTools(t *testing.T) {
	dir := t.TempDir()

	content := `---
name: no-tools
description: Agent with no tools.
tools: []
---
Just write text.
`
	if err := os.WriteFile(filepath.Join(dir, "no-tools.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	presets := loadPresetsFromDir(dir, "test")
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}

	p := presets[0]
	if !p.HasTools {
		t.Fatalf("expected HasTools=true for explicit empty tools")
	}
	if len(p.Tools) != 0 {
		t.Fatalf("expected empty tools slice, got %v", p.Tools)
	}
}

func TestLoadPresetNoTools(t *testing.T) {
	dir := t.TempDir()

	// No tools field at all = inherit all parent tools.
	content := `---
name: all-tools
description: Agent with all tools.
---
Use whatever tools you need.
`
	if err := os.WriteFile(filepath.Join(dir, "all-tools.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	presets := loadPresetsFromDir(dir, "test")
	if len(presets) != 1 {
		t.Fatalf("expected 1 preset, got %d", len(presets))
	}

	p := presets[0]
	if p.HasTools {
		t.Fatalf("expected HasTools=false when tools key is absent")
	}
}

func TestLoadPresetPriority(t *testing.T) {
	// Create two directories with overlapping preset names.
	projectDir := filepath.Join(t.TempDir(), ".agents", "agents")
	commonDir := filepath.Join(t.TempDir(), ".agents", "agents")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(commonDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Project version wins.
	projectContent := `---
name: myagent
description: Project version.
---
Project system prompt.
`
	commonContent := `---
name: myagent
description: Common version.
---
Common system prompt.
`
	if err := os.WriteFile(filepath.Join(projectDir, "myagent.md"), []byte(projectContent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(commonDir, "myagent.md"), []byte(commonContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// loadAgentPresets with cwd pointing to projectDir's parent.
	cwd := filepath.Dir(filepath.Dir(projectDir)) // go up from .agents/agents/ to project root
	homeDir := filepath.Dir(filepath.Dir(commonDir))
	presets := loadAgentPresets(homeDir, "", "", cwd)

	reg := NewPresetRegistry(presets)
	p, ok := reg.Lookup("myagent")
	if !ok {
		t.Fatalf("expected to find preset 'myagent'")
	}
	if p.Description != "Project version." {
		t.Fatalf("expected project version to win, got %q", p.Description)
	}
	if p.Source != "project" {
		t.Fatalf("expected source 'project', got %q", p.Source)
	}
}

func TestLoadPresetMissingDescription(t *testing.T) {
	dir := t.TempDir()

	content := `---
name: bad
---
No description.
`
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	presets := loadPresetsFromDir(dir, "test")
	if len(presets) != 0 {
		t.Fatalf("expected 0 presets for missing description, got %d", len(presets))
	}
}
