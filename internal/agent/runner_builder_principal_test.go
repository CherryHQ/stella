package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

type testWorkspaceViewer struct{ root string }

type failingWorkspaceViewer struct{ err error }

func (w failingWorkspaceViewer) WorkspaceView(context.Context, home.WorkspaceRequest) (home.WorkspaceView, error) {
	return home.WorkspaceView{}, w.err
}

func (w testWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	shared := home.WorkspaceView{
		SystemSkillRoot:      pkgsandbox.HomeAttachment{HomeID: "system", ReadOnly: true},
		SystemAgentSkillRoot: pkgsandbox.HomeAttachment{HomeID: "system-agent", ReadOnly: true},
	}
	if req.GroupID != "" {
		principal := GroupHomeDir(w.root, req.GroupID)
		agent := AgentDirInHome(principal, req.AgentID)
		if err := os.MkdirAll(agent, 0o755); err != nil {
			return home.WorkspaceView{}, err
		}
		shared.PrincipalRoot, shared.DataRoot, shared.AgentRoot = principal, filepath.Join(principal, "data"), agent
		shared.Principal = pkgsandbox.HomeAttachment{HomeID: "principal"}
		shared.Agent = pkgsandbox.HomeAttachment{HomeID: "agent"}
		return shared, nil
	}
	if req.UserID != "" {
		principal := UserHomeDir(w.root, req.UserID)
		agent := AgentDirInHome(principal, req.AgentID)
		if err := os.MkdirAll(agent, 0o755); err != nil {
			return home.WorkspaceView{}, err
		}
		shared.PrincipalRoot, shared.DataRoot, shared.AgentRoot = principal, filepath.Join(principal, "data"), agent
		shared.Principal = pkgsandbox.HomeAttachment{HomeID: "principal"}
		shared.Agent = pkgsandbox.HomeAttachment{HomeID: "agent"}
		return shared, nil
	}
	return shared, nil
}

func TestNewRunnerFuncUsesPrincipalWorkspace(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	snap := &config.Snapshot{AgentID: "a1", Provider: "anthropic", Model: "test-model", APIKey: "test-key"}
	snap.Workspace = t.TempDir()
	for _, tt := range []struct {
		name     string
		params   RunnerParams
		wantRoot string
		wantWork string
	}{
		{name: "personal", params: RunnerParams{UserID: "u1", AgentID: "a1"}, wantRoot: UserHomeDir(stellaHome, "u1"), wantWork: filepath.Join(stellaHome, "users", "u1", "agents", "a1")},
		{name: "group", params: RunnerParams{UserID: "g1", GroupID: "g1", AgentID: "a1"}, wantRoot: GroupHomeDir(stellaHome, "g1"), wantWork: filepath.Join(stellaHome, "users", "group-g1", "agents", "a1")},
		{name: "user-less", params: RunnerParams{AgentID: "a1"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var promptBuild plugins.SystemPromptContext
			build := newRunnerFunc(runnerBuilderConfig{
				Snap:            snap,
				WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
				PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
					promptBuild = build
					return nil, nil
				},
				ProviderStreamBuilder: func(_, _, _ string) (providers.StreamFunc, error) {
					return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
				},
				SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
			})
			builtRunner, err := build(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("build runner: %v", err)
			}
			t.Cleanup(func() { _ = builtRunner.Close() })
			if tt.name == "user-less" {
				if promptBuild.UserRoot == "" || filepath.Dir(promptBuild.UserRoot) != filepath.Join(stellaHome, runnerScratchDir) {
					t.Errorf("user-less root = %q, want disposable scratch", promptBuild.UserRoot)
				}
				for _, dir := range []string{filepath.Dir(promptBuild.UserRoot), promptBuild.UserRoot} {
					info, err := os.Stat(dir)
					if err != nil || info.Mode().Perm() != 0o700 {
						t.Fatalf("scratch permissions for %q = %v, %v; want 0700", dir, info, err)
					}
				}
				if err := os.WriteFile(filepath.Join(promptBuild.UserRoot, "owned"), []byte("ok"), 0o600); err != nil {
					t.Fatalf("scratch is not writable: %v", err)
				}
				impl := builtRunner.(*runner)
				workspaceRoot, err := filepath.EvalSymlinks(impl.session.Policy().Filesystem.WorkspaceRoot)
				promptRoot, promptErr := filepath.EvalSymlinks(promptBuild.UserRoot)
				if err != nil || promptErr != nil || impl.sandboxCfg.Paths.AgentRoot != snap.Workspace || workspaceRoot != promptRoot {
					t.Fatalf("definition/scratch roots = agent %q workspace %q scratch %q", impl.sandboxCfg.Paths.AgentRoot, impl.session.Policy().Filesystem.WorkspaceRoot, promptBuild.UserRoot)
				}
				if got := impl.session.Policy().Filesystem.Homes; len(got) != 2 || !got[0].ReadOnly || !got[1].ReadOnly {
					t.Fatalf("user-less Homes = %#v, want only read-only shared roots", got)
				}
				scratch := promptBuild.UserRoot
				if err := builtRunner.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(scratch); !os.IsNotExist(err) {
					t.Fatalf("scratch remains after Close: %v", err)
				}
			} else {
				if got := promptBuild.UserRoot; got != tt.wantRoot {
					t.Errorf("prompt UserRoot = %q, want %q", got, tt.wantRoot)
				}
				if got := promptBuild.WorkspaceRoot; got != tt.wantWork {
					t.Errorf("prompt WorkspaceRoot = %q, want %q", got, tt.wantWork)
				}
			}
		})
	}
}

func TestNewRunnerFuncPropagatesWorkspaceError(t *testing.T) {
	want := errors.New("Home unavailable")
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            &config.Snapshot{Provider: "anthropic", Model: "test"},
		WorkspaceViewer: failingWorkspaceViewer{err: want},
	})
	if _, err := build(context.Background(), RunnerParams{UserID: "u", AgentID: "a"}); !errors.Is(err, want) {
		t.Fatalf("runner error = %v, want %v", err, want)
	}
}

func TestNewRunnerFuncRejectsUserlessProject(t *testing.T) {
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            &config.Snapshot{Provider: "anthropic", Model: "test"},
		WorkspaceViewer: testWorkspaceViewer{root: t.TempDir()},
	})
	if _, err := build(context.Background(), RunnerParams{AgentID: "a", ProjectID: "p"}); err == nil {
		t.Fatal("user-less ProjectID was accepted")
	}
}

func TestNewRunnerFuncCleansUserlessScratchOnConstructionFailure(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	var scratch string
	build := newRunnerFunc(runnerBuilderConfig{
		Snap:            &config.Snapshot{AgentID: "a", Provider: "anthropic", Model: "test"},
		WorkspaceViewer: testWorkspaceViewer{root: stellaHome},
		PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
			scratch = build.UserRoot
			return nil, nil
		},
		ProviderStreamBuilder: func(_, _, _ string) (providers.StreamFunc, error) {
			return nil, errors.New("provider unavailable")
		},
		SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
	})
	if _, err := build(context.Background(), RunnerParams{AgentID: "a"}); err == nil {
		t.Fatal("runner construction succeeded")
	}
	if scratch == "" {
		t.Fatal("scratch was not created before construction failure")
	}
	if _, err := os.Stat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch remains after construction failure: %v", err)
	}
}
