package agent

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	delegatetool "github.com/CherryHQ/stella/internal/agent/delegate"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory"
	coreagent "github.com/CherryHQ/stella/pkg/agent"
	"github.com/CherryHQ/stella/pkg/ai"
	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	"github.com/CherryHQ/stella/resources/binaries"
)

type panicSnapshotOpener struct {
	root string
	gate chan struct{}
}

func (o *panicSnapshotOpener) OpenRoot(context.Context, home.WorkspaceRequest, home.RootScope, home.RootAccess) (home.RootOperations, error) {
	<-o.gate
	r, err := os.OpenRoot(o.root)
	if err != nil {
		o.gate <- struct{}{}
		return nil, err
	}
	return &panicSnapshotRoot{runnerTestRoot: runnerTestRoot{Root: r}, gate: o.gate}, nil
}

type panicSnapshotRoot struct {
	runnerTestRoot
	gate chan struct{}
}

func (*panicSnapshotRoot) Stat(context.Context, string) (fs.FileInfo, error) {
	panic("snapshot read panic")
}

func (r *panicSnapshotRoot) Close() error {
	err := r.runnerTestRoot.Close()
	r.gate <- struct{}{}
	return err
}

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
	if err := os.MkdirAll(filepath.Join(stellaHome, "users", "user-1", "data"), 0o700); err != nil {
		t.Fatalf("MkdirAll user data: %v", err)
	}
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
	resolveCalls := 0
	build := newRunnerFunc(withTestSkillDependencies(runnerBuilderConfig{
		Snap: snap,
		Home: testWorkspaceViewer{root: stellaHome},
		PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
			promptBuild = build
			return nil, nil
		},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
		ProjectResolver: func(ctx context.Context, projectID, userID, agentID string) (ProjectDescriptor, error) {
			resolveCalls++
			if projectID != "project-1" || userID != "user-1" || agentID != snap.AgentID {
				t.Fatalf("ProjectResolver called with projectID=%q userID=%q", projectID, userID)
			}
			if resolveCalls > 1 {
				return ProjectDescriptor{ID: projectID, UserID: userID, AgentID: agentID, Path: "changed/generation"}, nil
			}
			return ProjectDescriptor{ID: projectID, UserID: userID, AgentID: agentID, Path: "projects/app"}, nil
		},
	}))

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
	if promptBuild.UserID != "user-1" || promptBuild.AgentID != snap.AgentID {
		t.Errorf("prompt identity = (%q, %q), want (%q, %q)", promptBuild.UserID, promptBuild.AgentID, "user-1", snap.AgentID)
	}
	if resolveCalls != 1 {
		t.Fatalf("project resolved %d times, want exactly once", resolveCalls)
	}
}

func TestSnapshotAuthorizedProjectPanicReleasesOwnerGate(t *testing.T) {
	root := t.TempDir()
	opener := &panicSnapshotOpener{root: root, gate: make(chan struct{}, 1)}
	opener.gate <- struct{}{}
	resolve := func(_ context.Context, projectID, userID, agentID string) (ProjectDescriptor, error) {
		return ProjectDescriptor{ID: projectID, UserID: userID, AgentID: agentID, Path: "."}, nil
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("SnapshotAuthorizedProject did not propagate read panic")
			}
		}()
		_, _ = SnapshotAuthorizedProject(context.Background(), resolve, opener, "p", "u", "a")
	}()
	if len(opener.gate) != 1 {
		t.Fatal("owner gate remained held after panic")
	}
	reopened, err := opener.OpenRoot(context.Background(), home.WorkspaceRequest{}, home.RootAgentWorkspace, home.RootReadOnly)
	if err != nil {
		t.Fatalf("owner gate remained held after panic: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewRunnerFuncGuestHasMinimalPromptAndNoTools(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)
	snap := &config.Snapshot{AgentID: "agent-1", Provider: "anthropic", Model: "test-model", APIKey: "test-key", SystemPrompt: "Operator base prompt", Workspace: t.TempDir()}
	build := newRunnerFunc(withTestSkillDependencies(runnerBuilderConfig{
		Snap:     snap,
		ToolMode: coreagent.ToolModeCode,
		ProviderStreamBuilder: func(string, string, string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
		PromptSectionsBuilder: func(context.Context, plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
			t.Fatal("guest must not build prompt sections")
			return nil, nil
		},
	}))
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
	if impl.hookSet != nil || impl.toolLifecycle != nil {
		t.Fatal("guest runner initialized tool hooks or lifecycle")
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

func TestNewRunnerFuncRejectsToolModeSnapshotDrift(t *testing.T) {
	build := newRunnerFunc(runnerBuilderConfig{ToolMode: coreagent.ToolModeCode})
	_, err := build(context.Background(), RunnerParams{ToolMode: coreagent.ToolModeNative})
	if err == nil || !strings.Contains(err.Error(), "tool mode snapshot mismatch") {
		t.Fatalf("build error = %v, want tool mode snapshot mismatch", err)
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
	if err := os.MkdirAll(filepath.Join(stellaHome, "users", "user-1", "data"), 0o700); err != nil {
		t.Fatalf("MkdirAll user data: %v", err)
	}

	build := newRunnerFunc(withTestSkillDependencies(runnerBuilderConfig{
		Snap: snap,
		Home: testWorkspaceViewer{root: stellaHome},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
	}))

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
	if err := os.MkdirAll(filepath.Join(stellaHome, "users", "user-1", "data"), 0o700); err != nil {
		t.Fatalf("MkdirAll user data: %v", err)
	}

	var adapterBuilds int
	bridge := &rebuildingDelegateRunner{}
	build := newRunnerFunc(withTestSkillDependencies(runnerBuilderConfig{
		Snap: snap,
		Home: testWorkspaceViewer{root: stellaHome},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			if api != providerAPI {
				return nil, providers.ErrProviderNotFound
			}
			adapterBuilds++
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
	}))
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

	build := newRunnerFunc(withTestSkillDependencies(runnerBuilderConfig{
		Snap: snap,
		Home: testWorkspaceViewer{root: stellaHome},
		ProviderStreamBuilder: func(api, apiKey, baseURL string) (providers.StreamFunc, error) {
			return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
		},
	}))

	r, err := build(context.Background(), RunnerParams{UserID: "1"})
	if err != nil {
		t.Skipf("build runner: docker not available: %v", err)
	}
	if r == nil {
		t.Fatal("expected non-nil runner")
	}
}
