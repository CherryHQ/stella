package share_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/CherryHQ/stella/internal/home"
)

// testWorkspaceViewer is explicit test wiring, never a production fallback.
type testWorkspaceViewer struct{ root string }

func (w testWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	principal := filepath.Join(w.root, "users", req.UserID)
	if req.GroupID != "" {
		principal = filepath.Join(w.root, "users", "group-"+req.GroupID)
	}
	data, agent := filepath.Join(principal, "data"), filepath.Join(principal, "agents", req.AgentID)
	if err := os.MkdirAll(filepath.Join(data, "assets"), 0o755); err != nil {
		return home.WorkspaceView{}, err
	}
	if err := os.MkdirAll(agent, 0o755); err != nil {
		return home.WorkspaceView{}, err
	}
	return home.WorkspaceView{PrincipalRoot: principal, DataRoot: data, AgentRoot: agent}, nil
}
