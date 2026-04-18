package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/internal/embedded"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
	"github.com/vaayne/anna/plugins/sandbox/boxsh/boxshclient"
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

func (s *stubProvider) StreamSimple(context.Context, ai.Model, ai.Context, ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("stub")
}

func testProviderRegistryBuilder(api, apiKey, baseURL string) (*providers.Registry, error) {
	if api != "anthropic" {
		return nil, providers.ErrProviderNotFound
	}
	reg := providers.NewRegistry()
	reg.Register(&stubProvider{})
	return reg, nil
}

func testRunnerPaths(t *testing.T) (annaHome, workspace, userRoot string) {
	t.Helper()
	if !boxshclient.PlatformSupportsBoxsh() {
		t.Skip("sandbox backend requires boxsh support on this platform")
	}
	annaHome = t.TempDir()
	workspace = t.TempDir()
	userRoot = filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = writeMockRPCBoxsh(t, annaHome, false)
	return annaHome, workspace, userRoot
}

func writeMockRPCBoxsh(t *testing.T, annaHome string, exitAfterHandshake bool) string {
	t.Helper()
	_ = embedded.EnsureTools(annaHome)
	binDir := filepath.Join(annaHome, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	boxshPath := filepath.Join(binDir, "boxsh")
	commandsLog := filepath.Join(annaHome, "boxsh-commands.log")
	exitAfterInit := ""
	if exitAfterHandshake {
		exitAfterInit = "\n\t\t\tsleep 0.1\n\t\t\texit 0"
	}
	script := "#!/bin/bash\n" +
		"logfile=\"" + commandsLog + "\"\n" +
		"if [[ \"$1\" == \"--version\" ]]; then\n" +
		"\techo boxsh 2.0.1\n" +
		"\texit 0\n" +
		"fi\n" +
		"while read -r line; do\n" +
		"\tid=$(echo \"$line\" | grep -o '\"id\":[0-9]*' | cut -d: -f2)\n" +
		"\tif [[ \"$line\" == *'\"method\":\"initialize\"'* ]]; then\n" +
		"\t\techo \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"result\\\":{\\\"serverInfo\\\":{\\\"name\\\":\\\"boxsh\\\",\\\"version\\\":\\\"2.0.1\\\"},\\\"protocolVersion\\\":\\\"2024-11-05\\\"},\\\"id\\\":$id}\"" + exitAfterInit + "\n" +
		"\telif [[ \"$line\" == *'\"method\":\"tools/call\"'* ]]; then\n" +
		"\t\techo \"$line\" >> \"$logfile\"\n" +
		"\t\techo \"{\\\"jsonrpc\\\":\\\"2.0\\\",\\\"result\\\":{\\\"content\\\":[{\\\"type\\\":\\\"text\\\",\\\"text\\\":\\\"ok\\\"}],\\\"structuredContent\\\":{\\\"stdout\\\":\\\"ok\\\",\\\"stderr\\\":\\\"\\\",\\\"exit_code\\\":0}},\\\"id\\\":$id}\"\n" +
		"\tfi\n" +
		"done\n"
	if err := os.WriteFile(boxshPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return commandsLog
}

func withTestRunnerPaths(t *testing.T, cfg GoRunnerConfig) GoRunnerConfig {
	t.Helper()
	annaHome, workspace, userRoot := testRunnerPaths(t)
	cfg.AnnaHome = annaHome
	cfg.AgentRoot = workspace
	cfg.UserRoot = userRoot
	return cfg
}

func TestPrepareSandboxDockerMissingBinaryReturnsDockerError(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("DOCKER_BIN", "")

	workspace := t.TempDir()
	userRoot := filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := GoRunnerConfig{
		AnnaHome:  t.TempDir(),
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Sandbox: config.SandboxConfig{
			Backend: config.SandboxBackendDocker,
		},
	}
	err := prepareSandbox(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for docker backend with no docker binary")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected error to mention 'docker', got: %v", err)
	}
}

func TestPrepareSandboxLocalReturnsNil(t *testing.T) {
	workspace := t.TempDir()
	userRoot := filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := GoRunnerConfig{
		AnnaHome:  t.TempDir(),
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Sandbox: config.SandboxConfig{
			Backend: config.SandboxBackendLocal,
		},
	}
	if err := prepareSandbox(context.Background(), cfg); err != nil {
		t.Fatalf("expected nil for local backend, got: %v", err)
	}
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
	if result.Text != "bash" {
		t.Fatalf("filtered bash result = %q, want bash", result.Text)
	}
}

func TestNewGoRunnerRequiresConfig(t *testing.T) {
	tests := []struct {
		name string
		cfg  GoRunnerConfig
	}{
		{"missing api", GoRunnerConfig{Model: "m", APIKey: "k"}},
		{"missing model", GoRunnerConfig{API: "anthropic", APIKey: "k"}},
		{"missing api_key", GoRunnerConfig{API: "anthropic", Model: "m"}},
		{"missing workspace", GoRunnerConfig{API: "anthropic", Model: "m", APIKey: "k", UserRoot: "/tmp/user"}},
		{"missing user_data_dir", GoRunnerConfig{API: "anthropic", Model: "m", APIKey: "k", AgentRoot: "/tmp/workspace"}},
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
	r, err := NewGoRunner(context.Background(), withTestRunnerPaths(t, GoRunnerConfig{
		API:       "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		Providers: testProviderRegistryBuilder,
	}))
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

func TestNewGoRunnerPreflightExtractsManagedTools(t *testing.T) {
	if !boxshclient.PlatformSupportsBoxsh() {
		t.Skip("sandbox backend requires boxsh support on this platform")
	}
	if !slices.Contains(embedded.ToolNames(), "boxsh") {
		t.Skip("embedded boxsh binary not present; run mise run tools:download first")
	}
	annaHome := t.TempDir()
	workspace := t.TempDir()
	userRoot := filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:       "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		AnnaHome:  annaHome,
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Providers: testProviderRegistryBuilder,
	})
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	defer func() { _ = r.Close() }()

	if _, err := os.Stat(filepath.Join(annaHome, "bin", "boxsh")); err != nil {
		t.Fatalf("stat extracted boxsh: %v", err)
	}
}

func TestNewGoRunnerUsesBoxshCoreToolsAndCleansUp(t *testing.T) {
	if boxshclient.PlatformSupportsBoxsh() == false {
		t.Skip("boxsh integration only applies on linux/darwin")
	}

	annaHome := t.TempDir()
	workspace := t.TempDir()
	userRoot := filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	commandsLog := writeMockRPCBoxsh(t, annaHome, false)

	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:       "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		AnnaHome:  annaHome,
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Providers: testProviderRegistryBuilder,
	})
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}

	sessionDir := r.session.SessionDir()
	if sessionDir == "" {
		t.Fatal("expected boxsh session dir to be created")
	}
	if _, err := os.Stat(sessionDir); err != nil {
		t.Fatalf("stat session dir: %v", err)
	}

	result, err := r.tools.Execute(context.Background(), "bash", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("execute bash: %v", err)
	}
	if !strings.Contains(result, "exit:0") {
		t.Fatalf("expected normalized bash result, got %q", result)
	}

	logged, err := os.ReadFile(commandsLog)
	if err != nil {
		t.Fatalf("read commands log: %v", err)
	}
	if !strings.Contains(string(logged), `"method":"tools/call"`) {
		t.Fatalf("expected tools/call RPC in log, got %s", string(logged))
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("expected session dir cleanup, stat err = %v", err)
	}
}

func TestGoRunnerAliveTracksDeadBoxshBackend(t *testing.T) {
	if boxshclient.PlatformSupportsBoxsh() == false {
		t.Skip("boxsh integration only applies on linux/darwin")
	}

	annaHome := t.TempDir()
	workspace := t.TempDir()
	userRoot := filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = writeMockRPCBoxsh(t, annaHome, true)

	r, err := NewGoRunner(context.Background(), GoRunnerConfig{
		API:       "anthropic",
		Model:     "claude-sonnet-4-20250514",
		APIKey:    "test-key",
		AnnaHome:  annaHome,
		AgentRoot: workspace,
		UserRoot:  userRoot,
		Providers: testProviderRegistryBuilder,
	})
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}
	defer func() { _ = r.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for r.Alive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if r.Alive() {
		t.Fatal("expected runner to report dead after boxsh exits")
	}
}

// goRunnerFakeProvider implements stream.Provider for testing Chat() without real API calls.
type goRunnerFakeProvider struct {
	api    string
	events []ai.AssistantEvent
	err    error
}

func (f *goRunnerFakeProvider) API() string { return f.api }

func (f *goRunnerFakeProvider) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
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

func (f *goRunnerFakeProvider) StreamSimple(goCtx context.Context, _ ai.Model, _ ai.Context, opts ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return f.Stream(goCtx, ai.Model{}, ai.Context{}, opts.StreamOptions)
}

// newTestGoRunner creates a GoRunner wired to a fake provider.
func newTestGoRunner(t *testing.T, fp *goRunnerFakeProvider) *GoRunner {
	t.Helper()
	r, err := NewGoRunner(context.Background(), withTestRunnerPaths(t, GoRunnerConfig{
		API:       fp.api,
		Model:     "test-model",
		APIKey:    "test-key",
		Providers: testProviderRegistryBuilder,
	}))
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
	_, err := NewGoRunner(context.Background(), withTestRunnerPaths(t, GoRunnerConfig{
		API:       "nonexistent",
		Model:     "test-model",
		APIKey:    "test-key",
		Providers: testProviderRegistryBuilder,
	}))
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

func (f *sequentialFakeProvider) Stream(_ context.Context, _ ai.Model, _ ai.Context, _ ai.StreamOptions) (providers.AssistantEventStream, error) {
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

func (f *sequentialFakeProvider) StreamSimple(goCtx context.Context, _ ai.Model, _ ai.Context, opts ai.SimpleStreamOptions) (providers.AssistantEventStream, error) {
	return f.Stream(goCtx, ai.Model{}, ai.Context{}, opts.StreamOptions)
}

func TestChatToolUseLoop(t *testing.T) {
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

	cfg := withTestRunnerPaths(t, GoRunnerConfig{
		API:       fp.api,
		Model:     "test-model",
		APIKey:    "test-key",
		Providers: testProviderRegistryBuilder,
	})
	r, err := NewGoRunner(context.Background(), cfg)
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

func TestAliveReflectsSandboxSessionState(t *testing.T) {
	r, err := NewGoRunner(context.Background(), withTestRunnerPaths(t, GoRunnerConfig{
		API:       "anthropic",
		Model:     "test-model",
		APIKey:    "test-key",
		Providers: testProviderRegistryBuilder,
	}))
	if err != nil {
		t.Fatalf("NewGoRunner: %v", err)
	}

	if !r.Alive() {
		t.Error("Alive() should be true before Close")
	}

	_ = r.Close()

	if r.Alive() {
		t.Error("Alive() should be false after Close")
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
		{"read", "read", map[string]any{"path": "/tmp/test.go"}, "/tmp/test.go"},
		{"write", "write", map[string]any{"path": "/tmp/out.txt"}, "/tmp/out.txt"},
		{"edit", "edit", map[string]any{"path": "/tmp/edit.go"}, "/tmp/edit.go"},
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
