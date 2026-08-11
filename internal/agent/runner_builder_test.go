package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/resources/binaries"
)

type fakeStreamProvider struct{}

func (fakeStreamProvider) API() string { return "anthropic" }
func (fakeStreamProvider) Stream(context.Context, ai.Model, ai.Context, ai.StreamOptions) (providers.AssistantEventStream, error) {
	return nil, errors.New("not implemented")
}

type rebuildingDelegateRunner struct {
	build    NewRunnerFunc
	requests []delegatetool.SessionRunRequest
}

func (r *rebuildingDelegateRunner) RunDelegateSession(ctx context.Context, req delegatetool.SessionRunRequest) (delegatetool.SessionRunResult, error) {
	r.requests = append(r.requests, req)
	if req.SessionID == "" {
		req.SessionID = "managed-session"
	}
	child, err := r.build(ctx, RunnerParams{
		Model:          req.Model,
		UserID:         "user-1",
		AgentID:        "test-agent",
		SessionID:      req.SessionID,
		DelegateRunner: r,
	})
	if err != nil {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, err
	}
	if err := child.Close(); err != nil {
		return delegatetool.SessionRunResult{SessionID: req.SessionID}, err
	}
	return delegatetool.SessionRunResult{SessionID: req.SessionID, Output: "done", Complete: true}, nil
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
	if err := os.WriteFile(filepath.Join(userAgentDir, "AGENTS.md"), []byte("root instructions from runner builder"), 0o644); err != nil {
		t.Fatalf("WriteFile root AGENTS.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "AGENTS.md"), []byte("project instructions from runner builder"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	var promptBuild plugins.SystemPromptContext
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            snap,
		WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
		PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
			promptBuild = build
			return nil, nil
		},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
		ProjectResolver: func(ctx context.Context, projectID, userID, agentID string) (ProjectDescriptor, error) {
			if projectID != "project-1" || userID != "user-1" || agentID != snap.AgentID {
				t.Fatalf("ProjectResolver called with projectID=%q userID=%q", projectID, userID)
			}
			return ProjectDescriptor{ID: projectID, UserID: userID, AgentID: agentID, Path: "projects/app"}, nil
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

	if got := r.SystemPrompt(); !strings.Contains(got, "root instructions from runner builder") || !strings.Contains(got, "project instructions from runner builder") || strings.Contains(got, stellaHome) {
		t.Fatalf("expected logical root-to-leaf project context without host path, got:\n%s", got)
	}
	if got, want := promptBuild.WorkspaceRoot, userAgentDir; got != want {
		t.Errorf("prompt WorkspaceRoot = %q, want per-agent workspace %q", got, want)
	}
	if got, want := promptBuild.UserRoot, filepath.Dir(filepath.Dir(userAgentDir)); got != want {
		t.Errorf("prompt UserRoot = %q, want shared user home %q", got, want)
	}
}

func TestNewRunnerFuncGuestHasMinimalPromptAndNoTools(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	snap := &config.Snapshot{AgentID: "agent-1", Provider: "anthropic", Model: "test-model", APIKey: "test-key", SystemPrompt: "Operator base prompt", Workspace: t.TempDir()}
	build := newRunnerFunc(runnerBuilderConfig{
		Snap: snap,
		ProviderStreamBuilder: func(string, string, string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
		PromptSectionsBuilder: func(context.Context, plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
			t.Fatal("guest must not build prompt sections")
			return nil, nil
		},
	})
	r, err := build(context.Background(), RunnerParams{UserID: "11111111-1111-4111-8111-111111111111", GuestID: "11111111-1111-4111-8111-111111111111", AgentID: "agent-1", SessionID: "guest-session"})
	if err != nil {
		t.Fatalf("build guest runner: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	impl := r.(*runner)
	if got := impl.tools.Definitions(); len(got) != 0 {
		t.Fatalf("guest tool definitions = %#v, want none", got)
	}
	if impl.SandboxSession() != nil {
		t.Fatal("guest runner created a sandbox session")
	}
	system := r.SystemPrompt()
	for _, forbidden := range []string{"# Tools", "# Filesystem", "# Memories", "## User Profile", "## Agent Soul", "# Plugins", "# Project Context"} {
		if strings.Contains(system, forbidden) {
			t.Fatalf("guest prompt contains forbidden section %q:\n%s", forbidden, system)
		}
	}
	if !strings.Contains(system, "Operator base prompt") || !strings.Contains(system, "# Guest limitations") {
		t.Fatalf("unexpected guest prompt:\n%s", system)
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
		Snap:            snap,
		WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
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

func TestNewRunnerFuncManagedSessionsPreserveQualifiedModelRef(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	const (
		providerID    = "openrouter-production"
		providerAlias = "openai"
		providerAPI   = "openai"
		modelID       = "anthropic/claude-sonnet-4-6"
		modelRef      = providerID + "/" + modelID
	)
	snap := &config.Snapshot{
		AgentID:  "test-agent",
		Provider: providerAlias,
		Model:    providerAlias + "/" + modelID,
		Providers: map[string]config.ProviderCreds{
			providerAlias: {Type: providerAPI, APIKey: "test-key", ProviderID: providerID},
		},
		Workspace: t.TempDir(),
	}

	var adapterBuilds int
	bridge := &rebuildingDelegateRunner{}
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            snap,
		WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			if api != providerAPI {
				return nil, providers.ErrProviderNotFound
			}
			adapterBuilds++
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
	})
	bridge.build = build

	source, err := build(context.Background(), RunnerParams{
		UserID:         "user-1",
		AgentID:        snap.AgentID,
		SessionID:      "source-session",
		DelegateRunner: bridge,
	})
	if err != nil {
		t.Fatalf("build source runner: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	impl := source.(*runner)
	if got := impl.model.Provider; got != providerID {
		t.Fatalf("source model provider = %q, want canonical ID %q", got, providerID)
	}

	ctx := memory.WithSessionID(context.Background(), "source-session")
	created, err := impl.delegateTool.RunManagedSession(ctx, delegatetool.ManagedSessionRequest{Message: "create"})
	if err != nil {
		t.Fatalf("managed create: %v", err)
	}
	if _, err := impl.delegateTool.RunManagedSession(ctx, delegatetool.ManagedSessionRequest{SessionID: created.SessionID, Message: "send"}); err != nil {
		t.Fatalf("managed send: %v", err)
	}

	if len(bridge.requests) != 2 {
		t.Fatalf("managed requests = %d, want 2", len(bridge.requests))
	}
	for i, req := range bridge.requests {
		if req.Model != modelRef {
			t.Errorf("managed request %d model = %q, want %q", i, req.Model, modelRef)
		}
	}
	if adapterBuilds != 3 {
		t.Fatalf("provider adapter builds = %d, want source + create + send = 3", adapterBuilds)
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
		Snap:            snap,
		WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
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
