package share_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/recally"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingShareWorkspaceViewer struct {
	view  home.WorkspaceView
	err   error
	calls int
}

func (v *recordingShareWorkspaceViewer) WorkspaceView(context.Context, home.WorkspaceRequest) (home.WorkspaceView, error) {
	v.calls++
	return v.view, v.err
}

func TestShareArtifactUsesWorkspaceViewRoots(t *testing.T) {
	for _, tt := range []struct {
		name, scope, rootField string
	}{
		{name: "user scope", scope: "user", rootField: "data"},
		{name: "agent scope", scope: "agent", rootField: "agent"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db := dbtest.New(t)
			mem := memorytest.New()
			userID, agentID := seedShareUser(t, db, tt.name), "workspace-view-agent"
			if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "session", UserID: userID, AgentID: agentID}); err != nil {
				t.Fatal(err)
			}
			view := home.WorkspaceView{DataRoot: filepath.Join(t.TempDir(), "supplied-data"), AgentRoot: filepath.Join(t.TempDir(), "supplied-agent")}
			root := view.DataRoot
			if tt.rootField == "agent" {
				root = view.AgentRoot
			}
			if err := os.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "artifact.html"), []byte(tt.name), 0o644); err != nil {
				t.Fatal(err)
			}
			viewer := &recordingShareWorkspaceViewer{view: view}
			svc := sharepkg.NewService(sqlc.New(db), mem, recally.NewStore(db), mustAssets(t, t.TempDir(), nil), t.TempDir(), "http://stella.test", sharepkg.WithHomeWorkspace(viewer))
			created, err := mustAccess(t, svc, agentAuthority(t, userID, agentID)).ShareArtifact(ctx, "session", "artifact.html", tt.scope, agentID, "7d")
			if err != nil {
				t.Fatalf("ShareArtifact: %v", err)
			}
			if string(created.Share.Content) != tt.name || viewer.calls != 1 {
				t.Fatalf("content/calls = %q/%d, want %q/1", created.Share.Content, viewer.calls, tt.name)
			}
		})
	}
}

func TestShareArtifactWorkspaceViewFailuresAndGroupSessionsFailClosed(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mem := memorytest.New()
	userID, agentID := seedShareUser(t, db, "workspace-failure"), "workspace-failure-agent"
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "user-session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("home unavailable")
	viewer := &recordingShareWorkspaceViewer{err: want}
	svc := sharepkg.NewService(sqlc.New(db), mem, recally.NewStore(db), mustAssets(t, t.TempDir(), nil), t.TempDir(), "http://stella.test", sharepkg.WithHomeWorkspace(viewer))
	acc := mustAccess(t, svc, agentAuthority(t, userID, agentID))
	if _, err := acc.ShareArtifact(ctx, "user-session", "artifact.html", "agent", agentID, "7d"); !errors.Is(err, want) {
		t.Fatalf("workspace failure = %v, want viewer error", err)
	}

	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "group-session", UserID: userID, GroupID: "group", AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	viewer.calls = 0
	if _, err := acc.ShareArtifact(ctx, "group-session", "artifact.html", "agent", agentID, "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("group share = %v, want opaque ErrNotFound", err)
	}
	if viewer.calls != 0 {
		t.Fatalf("group share called workspace viewer %d times, want 0", viewer.calls)
	}
}
