package access

import (
	"context"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/config"
	"github.com/CherryHQ/stella/internal/home"
)

// testWorkspaceViewer preserves legacy path expectations only in old tests.
type testWorkspaceViewer struct{}

func (testWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	principal := filepath.Join(config.StellaHome(), "users", req.UserID)
	if req.GroupID != "" {
		principal = filepath.Join(config.StellaHome(), "users", "group-"+req.GroupID)
	}
	agent := filepath.Join(principal, "agents", req.AgentID)
	for _, dir := range []string{filepath.Join(principal, "data", "assets"), agent} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return home.WorkspaceView{}, err
		}
	}
	return home.WorkspaceView{PrincipalRoot: principal, DataRoot: filepath.Join(principal, "data"), AgentRoot: agent}, nil
}

func workspaceRootForScope(userID, agentID string, scope WorkspaceScope) string {
	view, _ := testWorkspaceViewer{}.WorkspaceView(context.Background(), home.WorkspaceRequest{UserID: userID, AgentID: agentID})
	if scope == WorkspaceScopeUser {
		return view.DataRoot
	}
	return view.AgentRoot
}
