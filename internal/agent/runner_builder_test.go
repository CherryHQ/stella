package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/resources/binaries"
)

type fakeStreamProvider struct{}

func (fakeStreamProvider) API() string { return "anthropic" }
func (fakeStreamProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}

func TestNewRunnerFuncPassesProjectRootToSystemPrompt(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	snap := &config.Snapshot{
		AgentID:      "test-agent",
		Provider:     "anthropic",
		Model:        "test-model",
		APIKey:       "test-key",
		SystemPrompt: "You are Stella.",
	}
	snap.Workspace = t.TempDir()

	// A project is owned by the agent, so it lives under the agent's private subdir
	// of the user home (#442).
	userAgentDir := filepath.Join(stellaHome, "users", "user-1", "agents", snap.AgentID)
	projectRoot := filepath.Join(userAgentDir, "projects", "app")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("project instructions from runner builder"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var promptBuild pkgplugins.SystemPromptContext
	build := newRunnerFunc(runnerBuilderConfig{
		Snap: snap,
		PromptSectionsBuilder: func(_ context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
			promptBuild = build
			return nil, nil
		},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
		ProjectResolver: func(ctx context.Context, projectID, userID string) (string, error) {
			if projectID != "project-1" || userID != "user-1" {
				t.Fatalf("ProjectResolver called with projectID=%q userID=%q", projectID, userID)
			}
			return projectRoot, nil
		},
	})

	r, err := build(context.Background(), RunnerParams{UserID: "user-1", AgentID: snap.AgentID, ProjectID: "project-1"})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Fatalf("Close runner: %v", err)
		}
	})

	if got := r.SystemPrompt(); !strings.Contains(got, "project instructions from runner builder") {
		t.Fatalf("expected system prompt to include project AGENTS.md content, got:\n%s", got)
	}
	if got, want := promptBuild.WorkspaceRoot, userAgentDir; got != want {
		t.Errorf("prompt WorkspaceRoot = %q, want per-agent workspace %q", got, want)
	}
	if got, want := promptBuild.UserRoot, filepath.Dir(filepath.Dir(userAgentDir)); got != want {
		t.Errorf("prompt UserRoot = %q, want shared user home %q", got, want)
	}
}

func TestNewRunnerFuncCarriesDeclaredModelInput(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	snap := &config.Snapshot{
		AgentID:   "test-agent",
		Provider:  "anthropic",
		Model:     "anthropic/text-only-model",
		APIKey:    "test-key",
		Providers: map[string]config.ProviderCreds{"anthropic": {Type: "anthropic", APIKey: "test-key"}},
		ModelInputs: map[config.ModelKey][]string{
			{Provider: "anthropic", Model: "text-only-model"}: {"text"},
		},
	}
	snap.Workspace = t.TempDir()

	build := newRunnerFunc(runnerBuilderConfig{
		Snap: snap,
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
	})

	r, err := build(context.Background(), RunnerParams{UserID: "user-1", AgentID: snap.AgentID})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	impl, ok := r.(*runner)
	if !ok {
		t.Fatalf("runner type = %T, want *runner", r)
	}
	if got := impl.model.ImageCapability(); got != ai.ImageUnsupported {
		t.Fatalf("model.ImageCapability() = %v, want ImageUnsupported (Input=%v)", got, impl.model.Input)
	}
}

func TestNewRunnerFunc(t *testing.T) {
	stellaHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stellaHome, "bin"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	_ = binaries.EnsureTools(stellaHome)
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	snap := &config.Snapshot{
		AgentID:  "test-agent",
		Provider: "anthropic",
		Model:    "test-model",
		APIKey:   "test-key",
	}
	snap.Workspace = t.TempDir()

	build := newRunnerFunc(runnerBuilderConfig{
		Snap: snap,
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
	})

	r, err := build(context.Background(), RunnerParams{UserID: "1"})
	if err != nil {
		t.Skipf("build runner: docker not available: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}

func TestStructuredGroupRunnerRequiresLargeContextAndUsesGroupSafePromptScope(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	const (
		groupID = "11111111-1111-4111-8111-111111111111"
		agentID = "test-agent"
	)
	snap := &config.Snapshot{
		AgentID:   agentID,
		Provider:  "provider-1",
		Model:     "provider-1/group-chat",
		Workspace: t.TempDir(),
		Providers: map[string]config.ProviderCreds{
			"provider-1": {
				Type:   "openai",
				APIKey: "test-key",
				Models: map[string]config.ProviderModel{
					"group-chat": {
						Enabled:       true,
						ContextWindow: config.GroupMemoryMinimumContextWindow - 1,
					},
				},
			},
		},
	}

	var promptBuild pkgplugins.SystemPromptContext
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:                  snap,
		StructuredGroupMemory: true,
		PromptSectionsBuilder: func(_ context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
			promptBuild = build
			return nil, nil
		},
		ProviderStreamBuilder: func(string, string, string) (providers.StreamFunc, error) {
			return nil, errors.New("provider builder reached")
		},
	})

	_, err := build(context.Background(), RunnerParams{
		UserID:  groupID,
		GroupID: groupID,
		AgentID: agentID,
	})
	if err == nil || !strings.Contains(err.Error(), "requires at least") {
		t.Fatalf("small-context group runner error = %v", err)
	}

	creds := snap.Providers["provider-1"]
	model := creds.Models["group-chat"]
	model.ContextWindow = config.GroupMemoryMinimumContextWindow
	creds.Models["group-chat"] = model
	snap.Providers["provider-1"] = creds

	_, err = build(context.Background(), RunnerParams{
		UserID:  groupID,
		GroupID: groupID,
		AgentID: agentID,
	})
	if err == nil || !strings.Contains(err.Error(), "provider builder reached") {
		t.Fatalf("valid-context group runner did not reach provider builder: %v", err)
	}
	if promptBuild.UserID != "" {
		t.Fatalf("group prompt UserID = %q, want empty synthetic principal", promptBuild.UserID)
	}
	if promptBuild.AgentID != agentID {
		t.Fatalf("group prompt AgentID = %q, want %q", promptBuild.AgentID, agentID)
	}
}

func TestStructuredGroupContextLimitDoesNotApplyToDM(t *testing.T) {
	snap := &config.Snapshot{
		AgentID:   "test-agent",
		Provider:  "provider-1",
		Model:     "provider-1/dm-model",
		Workspace: t.TempDir(),
		Providers: map[string]config.ProviderCreds{
			"provider-1": {
				Type:   "openai",
				APIKey: "test-key",
				Models: map[string]config.ProviderModel{
					"dm-model": {Enabled: true, ContextWindow: 32_000},
				},
			},
		},
	}
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:                  snap,
		StructuredGroupMemory: true,
		ProviderStreamBuilder: func(string, string, string) (providers.StreamFunc, error) {
			return nil, errors.New("provider builder reached")
		},
	})

	_, err := build(context.Background(), RunnerParams{UserID: "user-1", AgentID: snap.AgentID})
	if err == nil || !strings.Contains(err.Error(), "provider builder reached") {
		t.Fatalf("DM runner was blocked by group context policy: %v", err)
	}
}
