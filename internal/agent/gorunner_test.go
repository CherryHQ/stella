package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/config"
	"github.com/vaayne/anna/pkg/ai"
	"github.com/vaayne/anna/pkg/providers"
	"github.com/vaayne/anna/pkg/tools"
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
	annaHome = t.TempDir()
	workspace = t.TempDir()
	userRoot = filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	return annaHome, workspace, userRoot
}

func withTestRunnerPaths(t *testing.T, cfg GoRunnerConfig) GoRunnerConfig {
	t.Helper()
	annaHome, workspace, userRoot := testRunnerPaths(t)
	cfg.AnnaHome = annaHome
	cfg.AgentRoot = workspace
	cfg.UserRoot = userRoot
	return cfg
}

func TestPrepareSandboxDockerUnreachableDaemonReturnsDockerError(t *testing.T) {
	// Point the SDK at a unix socket path that cannot exist, so client.Ping
	// via Version() fails during Preflight regardless of the host's real
	// docker state.
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/anna-test-docker.sock")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	workspace := t.TempDir()
	userRoot := filepath.Join(workspace, "users", "1")
	if err := os.MkdirAll(userRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	cfg := GoRunnerConfig{
		AnnaHome:         t.TempDir(),
		AgentRoot:        workspace,
		UserRoot:         userRoot,
		Sandbox:          config.SandboxConfig{},
		SandboxBackendFn: func(_ context.Context) string { return config.SandboxBackendDocker },
	}
	err := prepareSandbox(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for docker backend with unreachable daemon")
	}
	if !strings.Contains(err.Error(), "docker") {
		t.Fatalf("expected error to mention 'docker', got: %v", err)
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
// Requires a reachable docker daemon since docker is now the only sandbox backend.
// Skips the test if the docker daemon is not reachable or container creation fails.
func newTestGoRunner(t *testing.T, fp *goRunnerFakeProvider) *GoRunner {
	t.Helper()
	r, err := NewGoRunner(context.Background(), withTestRunnerPaths(t, GoRunnerConfig{
		API:       fp.api,
		Model:     "test-model",
		APIKey:    "test-key",
		Providers: testProviderRegistryBuilder,
	}))
	if err != nil {
		t.Skipf("NewGoRunner: docker not available: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
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
