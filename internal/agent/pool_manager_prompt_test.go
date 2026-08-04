package agent

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/memory"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestPoolSnapshotPromptUsesPrincipalWorkspace(t *testing.T) {
	stellaHome := t.TempDir()
	t.Setenv("STELLA_HOME", stellaHome)
	config.ResetStellaHome()
	t.Cleanup(config.ResetStellaHome)

	snap := &config.Snapshot{AgentID: "a1", Workspace: "/agent-definition/a1"}
	for _, tt := range []struct {
		name string
		info session.Info
		want string
	}{
		{name: "personal", info: session.Info{UserID: "u1", AgentID: "a1"}, want: filepath.Join(stellaHome, "users", "u1", "agents", "a1")},
		{name: "group", info: session.Info{UserID: "g1", GroupID: "g1", AgentID: "a1"}, want: filepath.Join(stellaHome, "users", "group-g1", "agents", "a1")},
		{name: "user-less", info: session.Info{AgentID: "a1"}, want: snap.Workspace},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got pkgplugins.SystemPromptContext
			pm := &PoolManager{homeWorkspace: testWorkspaceViewer{root: stellaHome}, promptSectionsBuilder: func(_ context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
				got = build
				return nil, nil
			}}
			if _, err := pm.buildSnapshotPromptFunc(snap)(context.Background(), tt.info, memory.SessionSnapshot{}); err != nil {
				t.Fatal(err)
			}
			if got.WorkspaceRoot != tt.want {
				t.Errorf("WorkspaceRoot = %q, want %q", got.WorkspaceRoot, tt.want)
			}
		})
	}
}

func TestPoolSnapshotPromptPropagatesWorkspaceError(t *testing.T) {
	want := errors.New("Home unavailable")
	pm := &PoolManager{homeWorkspace: failingWorkspaceViewer{err: want}}
	_, err := pm.buildSnapshotPromptFunc(&config.Snapshot{AgentID: "a"})(context.Background(), session.Info{UserID: "u", AgentID: "a"}, memory.SessionSnapshot{})
	if !errors.Is(err, want) {
		t.Fatalf("snapshot prompt error = %v, want %v", err, want)
	}
}
