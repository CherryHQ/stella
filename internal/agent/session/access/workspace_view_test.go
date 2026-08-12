package access

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent/session"
	"github.com/CherryHQ/stella/internal/home"
)

type recordingWorkspaceViewer struct {
	req  home.WorkspaceRequest
	view home.WorkspaceView
	err  error
}

func (v *recordingWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	v.req = req
	return v.view, v.err
}

func TestResolveWorkspaceRootSelectsGroupView(t *testing.T) {
	v := &recordingWorkspaceViewer{view: home.WorkspaceView{DataRoot: "/group/data", AgentRoot: "/group/agent"}}
	info := session.Info{UserID: "same", GroupID: "same", AgentID: "a"}
	for scope, want := range map[WorkspaceScope]string{WorkspaceScopeUser: "/group/data", WorkspaceScopeAgent: "/group/agent"} {
		got, err := resolveWorkspaceRoot(context.Background(), v, info, scope)
		if err != nil || got != want {
			t.Fatalf("%s = %q, %v", scope, got, err)
		}
	}
	if v.req.GroupID != "same" || v.req.UserID != "same" || v.req.AgentID != "a" {
		t.Fatalf("request=%+v", v.req)
	}
}

func TestResolveWorkspaceRootFailsClosed(t *testing.T) {
	want := errors.New("tombstoned")
	_, err := resolveWorkspaceRoot(context.Background(), &recordingWorkspaceViewer{err: want}, session.Info{UserID: "u", AgentID: "a"}, WorkspaceScopeUser)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, want) {
		t.Fatalf("error=%v", err)
	}
}
