package agent

import (
	"context"
	"testing"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/hooks"
	"github.com/vaayne/anna/pkg/providers"
)

// fakeProviders is a minimal ProviderGetter for testing.
type fakeProviders struct{}

func (f *fakeProviders) Get(_ string) (providers.ProviderAdapter, bool) { return nil, false }

func TestNewRunner_NilProviders(t *testing.T) {
	_, err := NewRunner(RunnerConfig{})
	if err == nil {
		t.Error("expected error with nil providers")
	}
}

func TestNewRunner_Success(t *testing.T) {
	r, err := NewRunner(RunnerConfig{
		Providers: &fakeProviders{},
		Model:     ai.Model{ID: "claude-3", API: "anthropic"},
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
	meta := hooks.HookMeta{SessionID: "sess-1", UserID: 42}

	r, err := NewRunner(
		RunnerConfig{Providers: &fakeProviders{}},
		WithSystem("you are helpful"),
		WithMaxTurns(5),
		WithHooks(hs, meta),
		WithStreamOptions(ai.StreamOptions{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if r.system != "you are helpful" {
		t.Errorf("expected system prompt, got %q", r.system)
	}
	if r.maxTurns != 5 {
		t.Errorf("expected maxTurns=5, got %d", r.maxTurns)
	}
	if r.hooks != hs {
		t.Error("expected hooks to be set")
	}
	if r.hookMeta != meta {
		t.Errorf("expected hookMeta to be set, got %+v", r.hookMeta)
	}
}

func TestRunner_SetHookMeta(t *testing.T) {
	r, _ := NewRunner(RunnerConfig{Providers: &fakeProviders{}})
	meta := hooks.HookMeta{SessionID: "s1", UserID: 10}
	r.SetHookMeta(meta)
	if r.hookMeta != meta {
		t.Errorf("expected updated hookMeta, got %+v", r.hookMeta)
	}
}

func TestRunner_WithInterrupt(t *testing.T) {
	ch := make(chan struct{})
	r, err := NewRunner(
		RunnerConfig{Providers: &fakeProviders{}},
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
	r, _ := NewRunner(RunnerConfig{Providers: &fakeProviders{}})
	_, err := r.Continue(context.Background(), nil, func(LoopEvent) {})
	if err == nil {
		t.Error("expected error for empty history")
	}
}

func TestRunner_Continue_InvalidTail(t *testing.T) {
	r, _ := NewRunner(RunnerConfig{Providers: &fakeProviders{}})
	msgs := []ai.Message{ai.AssistantMessage{}}
	_, err := r.Continue(context.Background(), msgs, func(LoopEvent) {})
	if err == nil {
		t.Error("expected error for invalid tail (AssistantMessage)")
	}
}

func TestResolveBaseURL(t *testing.T) {
	// opts.BaseURL takes precedence.
	model := ai.Model{BaseURL: "http://model.base"}
	got := resolveBaseURL(model, ai.StreamOptions{BaseURL: "http://override"})
	if got != "http://override" {
		t.Errorf("expected override URL, got %q", got)
	}

	// Falls back to model.BaseURL when opts.BaseURL is empty.
	got = resolveBaseURL(model, ai.StreamOptions{})
	if got != "http://model.base" {
		t.Errorf("expected model base URL, got %q", got)
	}
}

func TestNewRunner_CopiesToolsAndDefs(t *testing.T) {
	tools := ToolSet{"foo": func(context.Context, ai.ToolCall) (ai.TextContent, error) {
		return ai.TextContent{Text: "ok"}, nil
	}}
	defs := []ai.ToolDefinition{{Name: "foo"}}

	r, err := NewRunner(RunnerConfig{
		Providers:       &fakeProviders{},
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
