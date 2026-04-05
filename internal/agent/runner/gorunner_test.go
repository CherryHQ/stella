package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	pluginproviders "github.com/vaayne/anna/plugins/providers"
)

func init() {
	// Register a stub provider so NewGoRunner can Build("anthropic", ...).
	// Tests replace the registry entry with their own fake after construction.
	pluginproviders.Register("anthropic", pluginproviders.ProviderMeta{Name: "Anthropic"}, func(cfg pluginproviders.ProviderConfig) providers.ProviderAdapter {
		return &stubProvider{}
	})
}

type stubProvider struct{}

func (s *stubProvider) API() string { return "anthropic" }
func (s *stubProvider) Stream(ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("stub")
}
func (s *stubProvider) StreamSimple(ai.Model, ai.Context, ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("stub")
}

func TestNewGoRunnerRequiresConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  GoRunnerConfig
	}{
		{"missing api", GoRunnerConfig{Model: "m", APIKey: "k"}},
		{"missing model", GoRunnerConfig{API: "anthropic", APIKey: "k"}},
		{"missing api_key", GoRunnerConfig{API: "anthropic", Model: "m"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewGoRunner(context.Background(), tt.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestNewGoRunnerSuccess(t *testing.T) {
	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:    "anthropic",
		Model:  "claude-sonnet-4-20250514",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Alive() {
		t.Error("new runner should be alive")
	}
	if err := r.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// goRunnerFakeProvider implements stream.Provider for testing Chat() without real API calls.
type goRunnerFakeProvider struct {
	api    string
	events []ai.AssistantEvent
	err    error
}

func (f *goRunnerFakeProvider) API() string { return f.api }

func (f *goRunnerFakeProvider) Stream(_ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := providers.NewChannelEventStream(len(f.events) + 1)
	go func() {
		for _, evt := range f.events {
			out.Emit(evt)
		}
		out.Finish(nil)
	}()
	return out, nil
}

func (f *goRunnerFakeProvider) StreamSimple(_ ai.Model, _ ai.Context, opts ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return f.Stream(ai.Model{}, ai.Context{}, opts.StreamOptions)
}

// newTestGoRunner creates a GoRunner wired to a fake provider.
func newTestGoRunner(t *testing.T, fp *goRunnerFakeProvider) *GoRunner {
	t.Helper()
	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:    fp.api,
		Model:  "test-model",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	r.reg.Register(fp)
	return r
}

func TestChatStreamsTextDeltas(t *testing.T) {
	fp := &goRunnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventStart{},
			ai.EventTextDelta{Text: "Hello "},
			ai.EventTextDelta{Text: "world"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestGoRunner(t, fp)

	ch := r.Chat(context.Background(), nil, "hi")

	var collected string
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("unexpected error: %v", evt.Err)
		}
		collected += evt.Text
	}

	if collected != "Hello world" {
		t.Errorf("collected = %q, want %q", collected, "Hello world")
	}
}

func TestChatStreamError(t *testing.T) {
	fp := &goRunnerFakeProvider{
		api: "anthropic",
		err: errors.New("provider boom"),
	}
	r := newTestGoRunner(t, fp)

	ch := r.Chat(context.Background(), nil, "hi")

	var gotErr error
	for evt := range ch {
		if evt.Err != nil {
			gotErr = evt.Err
			break
		}
	}

	if gotErr == nil {
		t.Fatal("expected error from stream")
	}
}

func TestChatUnknownProvider(t *testing.T) {
	_, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:    "nonexistent",
		Model:  "test-model",
		APIKey: "test-key",
	})
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestChatContextCancellation(t *testing.T) {
	fp := &goRunnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventTextDelta{Text: "ok"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestGoRunner(t, fp)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := r.Chat(ctx, nil, "hi")

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Chat channel did not close after context cancellation")
	}
}

func TestLastActivityUpdatesOnChat(t *testing.T) {
	fp := &goRunnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventTextDelta{Text: "ok"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestGoRunner(t, fp)

	before := time.Now()
	time.Sleep(1 * time.Millisecond)

	ch := r.Chat(context.Background(), nil, "hi")
	for range ch {
	}

	if r.LastActivity().Before(before) {
		t.Errorf("LastActivity %v should be after %v", r.LastActivity(), before)
	}
}

// sequentialFakeProvider returns different event sequences on successive Stream calls.
type sequentialFakeProvider struct {
	api    string
	rounds [][]ai.AssistantEvent
	call   int
	mu     sync.Mutex
}

func (f *sequentialFakeProvider) API() string { return f.api }

func (f *sequentialFakeProvider) Stream(_ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
	f.mu.Lock()
	idx := f.call
	f.call++
	f.mu.Unlock()

	events := f.rounds[idx]
	out := providers.NewChannelEventStream(len(events) + 1)
	go func() {
		for _, evt := range events {
			out.Emit(evt)
		}
		out.Finish(nil)
	}()
	return out, nil
}

func (f *sequentialFakeProvider) StreamSimple(_ ai.Model, _ ai.Context, opts ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return f.Stream(ai.Model{}, ai.Context{}, opts.StreamOptions)
}

func TestChatToolUseLoop(t *testing.T) {
	dir := t.TempDir()
	fp := &sequentialFakeProvider{
		api: "anthropic",
		rounds: [][]ai.AssistantEvent{
			{
				ai.EventToolCallDelta{ID: "tc_1", Name: "bash"},
				ai.EventToolCallDelta{ID: "tc_1", Arguments: `{"command": "echo hello"}`},
				ai.EventStop{Reason: ai.StopReasonToolUse},
			},
			{
				ai.EventTextDelta{Text: "The result is hello"},
				ai.EventStop{Reason: ai.StopReasonStop},
			},
		},
	}

	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:     fp.api,
		Model:   "test-model",
		APIKey:  "test-key",
		WorkDir: dir,
	})
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	r.reg.Register(fp)

	ch := r.Chat(context.Background(), nil, "run echo hello")

	var collected string
	for evt := range ch {
		if evt.Err != nil {
			t.Fatalf("unexpected error: %v", evt.Err)
		}
		collected += evt.Text
	}

	if collected != "The result is hello" {
		t.Errorf("collected = %q, want %q", collected, "The result is hello")
	}
}

func TestAliveAlwaysTrue(t *testing.T) {
	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:    "anthropic",
		Model:  "test-model",
		APIKey: "test-key",
	})
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}

	if !r.Alive() {
		t.Error("Alive() should be true before Close")
	}

	_ = r.Close()

	if !r.Alive() {
		t.Error("Alive() should still be true after Close (no subprocess)")
	}
}

func TestSummarizeToolInput(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		args     map[string]any
		want     string
	}{
		{"bash short", "bash", map[string]any{"command": "ls -la"}, "ls -la"},
		{"bash long", "bash", map[string]any{"command": "echo " + string(make([]byte, 100))}, ""},
		{"read", "read", map[string]any{"file_path": "/tmp/test.go"}, "/tmp/test.go"},
		{"write", "write", map[string]any{"file_path": "/tmp/out.txt"}, "/tmp/out.txt"},
		{"edit", "edit", map[string]any{"file_path": "/tmp/edit.go"}, "/tmp/edit.go"},
		{"unknown tool", "unknown", map[string]any{"foo": "bar"}, ""},
		{"bash no command", "bash", map[string]any{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeToolInput(tt.toolName, tt.args)
			if tt.name == "bash long" {
				if len(got) > 84 { // 80 + "..."
					t.Errorf("long bash should be truncated, got len %d", len(got))
				}
				return
			}
			if got != tt.want {
				t.Errorf("summarizeToolInput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummarizeToolResult(t *testing.T) {
	tests := []struct {
		name    string
		result  ai.ToolResultMessage
		want    string
		contain string // if non-empty, check Contains instead of exact match
	}{
		{
			name: "empty content",
			result: ai.ToolResultMessage{
				ToolName: "bash",
				Content:  nil,
			},
			want: "",
		},
		{
			name: "short result inline",
			result: ai.ToolResultMessage{
				ToolName: "bash",
				Content:  []ai.ContentBlock{ai.TextContent{Text: "hello world"}},
			},
			want: "hello world",
		},
		{
			name: "multiline short result first line",
			result: ai.ToolResultMessage{
				ToolName: "bash",
				Content:  []ai.ContentBlock{ai.TextContent{Text: "ok\ndone"}},
			},
			want: "ok",
		},
		{
			name: "long multiline shows line count",
			result: ai.ToolResultMessage{
				ToolName: "read",
				Content:  []ai.ContentBlock{ai.TextContent{Text: "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\nline11\nline12\nline13\nline14\nline15"}},
			},
			contain: "15 lines",
		},
		{
			name: "error shows first line",
			result: ai.ToolResultMessage{
				ToolName: "bash",
				IsError:  true,
				Content:  []ai.ContentBlock{ai.TextContent{Text: "permission denied\nsome stack trace"}},
			},
			want: "permission denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeToolResult(tt.result)
			if tt.contain != "" {
				if got != tt.contain {
					t.Errorf("summarizeToolResult() = %q, want %q", got, tt.contain)
				}
			} else if got != tt.want {
				t.Errorf("summarizeToolResult() = %q, want %q", got, tt.want)
			}
		})
	}
}
