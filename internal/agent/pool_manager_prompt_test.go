package agent

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/config"
	pkgplugins "github.com/CherryHQ/stella/pkg/plugins"
)

func TestPoolPromptSectionsUsesPerAgentWorkspace(t *testing.T) {
	snap := &config.Snapshot{AgentID: "a1", Workspace: "/agent-definition/a1"}
	for _, tt := range []struct {
		name     string
		info     session.Info
		userRoot string
		want     string
	}{
		{
			name:     "personal home",
			info:     session.Info{UserID: "u1", AgentID: "a1"},
			userRoot: "/stella/users/u1",
			want:     "/stella/users/u1/agents/a1",
		},
		{
			name:     "group home",
			info:     session.Info{UserID: "g1", GroupID: "g1", AgentID: "a1"},
			userRoot: "/stella/users/group-g1",
			want:     "/stella/users/group-g1/agents/a1",
		},
		{
			name: "user-less fallback",
			info: session.Info{AgentID: "a1"},
			want: "/agent-definition/a1",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got pkgplugins.SystemPromptContext
			pm := &PoolManager{
				promptSectionsBuilder: func(_ context.Context, build pkgplugins.SystemPromptContext) ([]pkgplugins.SystemPromptSection, error) {
					got = build
					return nil, nil
				},
			}
			pm.promptSections(context.Background(), snap, tt.info, tt.userRoot)
			if got.WorkspaceRoot != tt.want {
				t.Errorf("WorkspaceRoot = %q, want %q", got.WorkspaceRoot, tt.want)
			}
		})
	}
}
