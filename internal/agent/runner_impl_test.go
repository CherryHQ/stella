package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CherryHQ/stella/internal/agent/sandbox"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/pkg/renderrefs"
	"github.com/CherryHQ/stella/pkg/tools"
)

type stubProvider struct{}

type stubTool struct{ name string }

func (s *stubTool) Definition() tools.Definition {
	return tools.Definition{Name: s.name, Description: "stub tool"}
}

func (s *stubTool) Execute(context.Context, map[string]any) (string, error) {
	return s.name, nil
}

func (s *stubProvider) API() string { return "anthropic" }
func (s *stubProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("stub")
}

func testProviderStreamBuilder(api, apiKey, baseURL string) (providers.StreamFunc, error) {
	if api != "anthropic" {
		return nil, providers.ErrProviderNotFound
	}
	return providers.AdapterStreamFunc(&stubProvider{}), nil
}

func testRunnerPaths(t *testing.T) (stellaHome, workspace, userRoot string) {
	t.Helper()
	stellaHome = t.TempDir()
	workspace = t.TempDir()
	userRoot = filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return stellaHome, workspace, userRoot
}

func withTestRunnerPaths(t *testing.T, cfg runnerConfig) runnerConfig {
	t.Helper()
	stellaHome, workspace, userRoot := testRunnerPaths(t)
	cfg.Sandbox.Paths.StellaHome = stellaHome
	cfg.Sandbox.Paths.AgentRoot = workspace
	cfg.Sandbox.Paths.UserRoot = userRoot
	return cfg
}

func TestFilterRunnerTools(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(&stubTool{name: "bash"})
	reg.Register(&stubTool{name: "notify"})
	reg.Register(&stubTool{name: "scheduler"})

	set, defs, err := filterRunnerTools(reg, []string{"notify", "scheduler"})
	if err != nil {
		t.Fatalf("filterRunnerTools: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "bash" {
		t.Fatalf("defs = %#v, want only bash", defs)
	}
	if _, ok := set["notify"]; ok {
		t.Fatal("notify should be excluded")
	}
	if _, ok := set["scheduler"]; ok {
		t.Fatal("scheduler should be excluded")
	}
	if _, ok := set["bash"]; !ok {
		t.Fatal("bash should remain available")
	}

	result, err := set["bash"](context.Background(), ai.ToolCall{Name: "bash"})
	if err != nil {
		t.Fatalf("execute filtered bash tool: %v", err)
	}
	if ai.FlattenText(result) != "bash" {
		t.Fatalf("filtered bash result = %q, want bash", ai.FlattenText(result))
	}
}

func TestNewRunnerRequiresConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  runnerConfig
	}{
		{"missing api", runnerConfig{Provider: providerConfig{Model: "m", APIKey: "k"}}},
		{"missing model", runnerConfig{Provider: providerConfig{API: "anthropic", APIKey: "k"}}},
		{"missing api_key", runnerConfig{Provider: providerConfig{API: "anthropic", Model: "m"}}},
		{"missing workspace", runnerConfig{Provider: providerConfig{API: "anthropic", Model: "m", APIKey: "k"}, Sandbox: sandbox.Config{Paths: sandbox.Paths{UserRoot: "/tmp/user"}}}},
		{"missing user_data_dir", runnerConfig{Provider: providerConfig{API: "anthropic", Model: "m", APIKey: "k"}, Sandbox: sandbox.Config{Paths: sandbox.Paths{AgentRoot: "/tmp/workspace"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newRunner(context.Background(), tt.cfg)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// runnerFakeProvider implements stream.Provider for testing Chat() without real API calls.
type runnerFakeProvider struct {
	api    string
	events []ai.AssistantEvent
	err    error
}

func (f *runnerFakeProvider) API() string { return f.api }

func (f *runnerFakeProvider) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
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

func TestNewAgentRunnerLimitsKnowledgeSearchPerRequest(t *testing.T) {
	var executed atomic.Int32

	stream := func(_ context.Context, _ ai.Model, request ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
		resultCount := 0
		for _, message := range request.Messages {
			if result, ok := message.(ai.ToolResultMessage); ok && result.ToolName == knowledgeSearchToolName {
				resultCount++
			}
		}

		out := providers.NewChannelEventStream(8)
		go func() {
			// Deliberately request three searches; only the first two may reach
			// the Knowledge Base implementation for this user request.
			if resultCount < 3 {
				out.Emit(ai.EventToolCallDelta{
					ID:        fmt.Sprintf("knowledge_call_%d", resultCount+1),
					Name:      knowledgeSearchToolName,
					Arguments: `{"query":"test"}`,
				})
				out.Emit(ai.EventStop{Reason: ai.StopReasonToolUse})
			} else {
				out.Emit(ai.EventTextDelta{Text: "done"})
				out.Emit(ai.EventStop{Reason: ai.StopReasonStop})
			}
			out.Finish(nil)
		}()
		return out, nil
	}

	coreRunner, err := newAgentRunnerWithTools(
		stream,
		ai.Model{API: "fake", Name: "stub"},
		ai.StreamOptions{},
		"",
		nil,
		nil,
		coreagent.ToolSet{
			knowledgeSearchToolName: func(_ context.Context, _ ai.ToolCall) ([]ai.ContentBlock, error) {
				executed.Add(1)
				return []ai.ContentBlock{ai.TextContent{Text: `{"results":[]}`}}, nil
			},
		},
		[]tools.Definition{{Name: knowledgeSearchToolName}},
	)
	if err != nil {
		t.Fatalf("newAgentRunnerWithTools: %v", err)
	}

	for runNumber := 1; runNumber <= 2; runNumber++ {
		history, err := coreRunner.Run(
			context.Background(),
			[]ai.Message{ai.UserMessage{Content: "search"}},
			nil,
		)
		if err != nil {
			t.Fatalf("run %d: %v", runNumber, err)
		}
		if got, want := executed.Load(), int32(runNumber*knowledgeSearchMaxCallsPerRequest); got != want {
			t.Fatalf("executed after run %d = %d, want %d", runNumber, got, want)
		}

		var lastResult ai.ToolResultMessage
		for _, message := range history {
			if result, ok := message.(ai.ToolResultMessage); ok {
				lastResult = result
			}
		}
		if !lastResult.IsError ||
			!strings.Contains(ai.FlattenText(lastResult.Content), "at most 2 times") {
			t.Fatalf("run %d last result = %+v, want limit error", runNumber, lastResult)
		}
	}
}

// newTestRunner creates a runner wired to a fake provider.
// Requires a reachable docker daemon since docker is now the only sandbox backend.
// Skips the test if the docker daemon is not reachable or container creation fails.
func newTestRunner(t *testing.T, fp *runnerFakeProvider) *runner {
	t.Helper()
	builder := func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
		if api != fp.api {
			return nil, providers.ErrProviderNotFound
		}
		return providers.AdapterStreamFunc(fp), nil
	}
	r, err := newRunner(context.Background(), withTestRunnerPaths(t, runnerConfig{
		Provider: providerConfig{
			API:     fp.api,
			Model:   "test-model",
			APIKey:  "test-key",
			Builder: builder,
		},
	}))
	if err != nil {
		t.Skipf("newRunner: docker not available: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestChatStreamsTextDeltas(t *testing.T) {
	fp := &runnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventStart{},
			ai.EventTextDelta{Text: "Hello "},
			ai.EventTextDelta{Text: "world"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestRunner(t, fp)

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
	fp := &runnerFakeProvider{
		api: "anthropic",
		err: errors.New("provider boom"),
	}
	r := newTestRunner(t, fp)

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
	_, err := newRunner(context.Background(), withTestRunnerPaths(t, runnerConfig{
		Provider: providerConfig{
			API:     "nonexistent",
			Model:   "test-model",
			APIKey:  "test-key",
			Builder: testProviderStreamBuilder,
		},
	}))
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestChatContextCancellation(t *testing.T) {
	fp := &runnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventTextDelta{Text: "ok"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestRunner(t, fp)

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
	fp := &runnerFakeProvider{
		api: "anthropic",
		events: []ai.AssistantEvent{
			ai.EventTextDelta{Text: "ok"},
			ai.EventStop{Reason: ai.StopReasonStop},
		},
	}
	r := newTestRunner(t, fp)

	before := time.Now()
	time.Sleep(1 * time.Millisecond)

	ch := r.Chat(context.Background(), nil, "hi")
	for range ch {
	}

	if r.LastActivity().Before(before) {
		t.Errorf("LastActivity %v should be after %v", r.LastActivity(), before)
	}
}

func TestConvertLoopEventStripsMalformedSentinelFromStore(t *testing.T) {
	// A truncated/corrupt sentinel yields no ref, but the raw marker must still be
	// scrubbed from the persisted result so a replay never feeds it to the model.
	text := "created task\n::stella-ref/v1::{\"v\":1,\"type\":\"ta"

	events := convertLoopEvent(coreagent.ToolFinished{Result: ai.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
	}})
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if len(events[0].References) != 0 {
		t.Fatalf("malformed sentinel produced refs: %#v", events[0].References)
	}
	stored, ok := events[1].Store.(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("second event Store = %T, want ai.ToolResultMessage", events[1].Store)
	}
	for _, block := range stored.Content {
		if tc, ok := block.(ai.TextContent); ok && strings.Contains(tc.Text, "::stella-ref/v1::") {
			t.Fatalf("stored result leaked malformed sentinel: %q", tc.Text)
		}
	}
}

func TestConvertLoopEventStripsRenderableReferences(t *testing.T) {
	ref := renderrefs.Reference{
		V:    1,
		Type: "task",
		ID:   "task-1",
		Preview: &renderrefs.Preview{
			Title:  "Ship it",
			Status: "open",
		},
	}
	var sb strings.Builder
	if err := renderrefs.Emit(&sb, ref); err != nil {
		t.Fatalf("Emit: %v", err)
	}
	text := "created task\n" + sb.String()

	events := convertLoopEvent(coreagent.ToolFinished{Result: ai.ToolResultMessage{
		ToolCallID: "call-1",
		ToolName:   "bash",
		Content:    []ai.ContentBlock{ai.TextContent{Text: text}},
	}})
	if len(events) != 2 {
		t.Fatalf("events len = %d, want 2", len(events))
	}
	if events[0].ToolUse == nil {
		t.Fatal("first event missing tool use")
	}
	if strings.Contains(events[0].ToolUse.Content, "::stella-ref/v1::") {
		t.Fatalf("tool content leaked sentinel: %q", events[0].ToolUse.Content)
	}
	// References live only on the tool event now; the event-level field is fanned
	// out later by the coordinator, not set here.
	if events[0].References != nil {
		t.Fatalf("event-level references should be unset, got %#v", events[0].References)
	}
	if len(events[0].ToolUse.References) != 1 || events[0].ToolUse.References[0].ID != "task-1" {
		t.Fatalf("tool references = %#v", events[0].ToolUse.References)
	}

	// The persisted tool result must be stripped too, or a replay would feed the
	// sentinel back to the model.
	stored, ok := events[1].Store.(ai.ToolResultMessage)
	if !ok {
		t.Fatalf("second event Store = %T, want ai.ToolResultMessage", events[1].Store)
	}
	for _, block := range stored.Content {
		if tc, ok := block.(ai.TextContent); ok && strings.Contains(tc.Text, "::stella-ref/v1::") {
			t.Fatalf("stored result leaked sentinel: %q", tc.Text)
		}
	}
	if len(stored.References) != 1 || stored.References[0].ID != "task-1" {
		t.Fatalf("stored references = %#v", stored.References)
	}
}
