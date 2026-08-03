package agent

import (
	"context"
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
			pm := &PoolManager{promptSectionsBuilder: func(_ context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
				got = build
				return nil, nil
			}}
			pm.buildSnapshotPromptFunc(snap)(context.Background(), tt.info, memory.SessionSnapshot{})
			if got.WorkspaceRoot != tt.want {
				t.Errorf("WorkspaceRoot = %q, want %q", got.WorkspaceRoot, tt.want)
			}
		})
	}
}
