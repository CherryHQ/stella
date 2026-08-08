package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/hooks"
	"github.com/CherryHQ/stella/pkg/providers"
)

// fakeStream is a minimal StreamFunc for testing that returns an error.
var fakeStream providers.StreamFunc = func(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, nil
}

func TestNewRunner_NilStream(t *testing.T) {
	_, err := NewRunner(RunnerConfig{})
	if err == nil {
		t.Error("expected error with nil stream")
	}
}

func TestNewRunner_Success(t *testing.T) {
	r, err := NewRunner(RunnerConfig{
		Stream: fakeStream,
		Model:  ai.Model{ID: "claude-3", API: "anthropic"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestNewRunner_WithOptions(t *testing.T) {
	hs := hooks.NewHookSet(nil)
	meta := hooks.HookMeta{SessionID: "sess-1", UserID: "42"}

	r, err := NewRunner(
		RunnerConfig{Stream: fakeStream},
		WithSystem("you are helpful"),
		WithHooks(hs, meta),
		WithStreamOptions(ai.StreamOptions{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.system != "you are helpful" {
		t.Errorf("expected system prompt, got %q", r.system)
	}
	if r.hooks != hs {
		t.Error("expected hooks to be set")
	}
	if r.hookMeta != meta {
		t.Errorf("expected hookMeta to be set, got %+v", r.hookMeta)
	}
}

func TestRunner_SetHookMeta(t *testing.T) {
	r, _ := NewRunner(RunnerConfig{Stream: fakeStream})
	meta := hooks.HookMeta{SessionID: "s1", UserID: "10"}
	r.SetHookMeta(meta)
	if r.hookMeta != meta {
		t.Errorf("expected updated hookMeta, got %+v", r.hookMeta)
	}
}

func TestRunner_WithInterrupt(t *testing.T) {
	ch := make(chan struct{})
	r, err := NewRunner(
		RunnerConfig{Stream: fakeStream},
		WithInterrupt(ch),
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.interrupt != ch {
		t.Error("expected interrupt channel to be set")
	}
}

func TestRunner_Continue_EmptyHistory(t *testing.T) {
	r, _ := NewRunner(RunnerConfig{Stream: fakeStream})
	_, err := r.Continue(context.Background(), nil, func(LoopEvent) {})
	if err == nil {
		t.Error("expected error for empty history")
	}
}

func TestRunner_Continue_InvalidTail(t *testing.T) {
	r, _ := NewRunner(RunnerConfig{Stream: fakeStream})
	msgs := []ai.Message{ai.AssistantMessage{}}
	_, err := r.Continue(context.Background(), msgs, func(LoopEvent) {})
	if err == nil {
		t.Error("expected error for invalid tail (AssistantMessage)")
	}
}

func TestNewRunner_CopiesToolsAndDefs(t *testing.T) {
	tools := ToolSet{"foo": func(context.Context, ai.ToolCall) ([]ai.ContentBlock, error) {
		return []ai.ContentBlock{ai.TextContent{Text: "ok"}}, nil
	}}
	defs := []ai.ToolDefinition{{Name: "foo"}}

	r, err := NewRunner(RunnerConfig{
		Stream:          fakeStream,
		Tools:           tools,
		ToolDefinitions: defs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(r.tools))
	}
	if len(r.toolDefs) != 1 {
		t.Errorf("expected 1 def, got %d", len(r.toolDefs))
	}
}

func TestNewRunnerCopiesPositiveToolCallLimits(t *testing.T) {
	limits := map[string]int{"library.search": 2, "ignored": 0}
	r, err := NewRunner(RunnerConfig{Stream: fakeStream, ToolCallLimits: limits})
	if err != nil {
		t.Fatal(err)
	}
	limits["library.search"] = 99
	if got := r.toolCallLimits["library.search"]; got != 2 {
		t.Fatalf("copied limit = %d, want 2", got)
	}
	if _, ok := r.toolCallLimits["ignored"]; ok {
		t.Fatal("non-positive call limit was retained")
	}
}
