package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/pkg/plugins"
	"github.com/CherryHQ/stella/pkg/providers"
)

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
		{name: "user-less", params: RunnerParams{AgentID: "a1"}, wantRoot: snap.Workspace, wantWork: snap.Workspace},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var promptBuild plugins.SystemPromptContext
			build := newRunnerFunc(runnerBuilderConfig{
				Snap: snap,
				PromptSectionsBuilder: func(_ context.Context, build plugins.SystemPromptContext) ([]plugins.SystemPromptSection, error) {
					promptBuild = build
					return nil, nil
				},
				ProviderStreamBuilder: func(_, _, _ string) (providers.StreamFunc, error) {
					return providers.AdapterStreamFunc(fakeStreamProvider{}), nil
				},
				SandboxBackendFn: func(context.Context) string { return config.SandboxBackendNone },
			})
			runner, err := build(context.Background(), tt.params)
			if err != nil {
				t.Fatalf("build runner: %v", err)
			}
			t.Cleanup(func() { _ = runner.Close() })
			if got := promptBuild.UserRoot; got != tt.wantRoot {
				t.Errorf("prompt UserRoot = %q, want %q", got, tt.wantRoot)
			}
			if got := promptBuild.WorkspaceRoot; got != tt.wantWork {
				t.Errorf("prompt WorkspaceRoot = %q, want %q", got, tt.wantWork)
			}
		})
	}
}
