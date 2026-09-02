package share_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/library/recally"
	"github.com/CherryHQ/stella/internal/memory"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/platform/home"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type recordingShareRootOpener struct {
	view   home.WorkspaceView
	err    error
	calls  int
	scope  home.RootScope
	access home.RootAccess
}

func (v *recordingShareRootOpener) OpenRoot(_ context.Context, _ home.WorkspaceRequest, scope home.RootScope, access home.RootAccess) (home.RootOperations, error) {
	v.calls++
	v.scope, v.access = scope, access
	if v.err != nil {
		return nil, v.err
	}
	dir := v.view.AgentRoot
	if scope == home.RootPrincipalData {
		dir = v.view.DataRoot
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return testRoot{Root: root}, nil
}

func TestShareArtifactUsesReadOnlyWorkspaceRoots(t *testing.T) {
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
			viewer := &recordingShareRootOpener{view: view}
			svc := sharepkg.NewService(sqlc.New(db), mem, recally.NewStore(db), t.TempDir(), "http://stella.test", sharepkg.WithHomeWorkspace(viewer), sharepkg.WithAgentAccess(allowAgentRead{}))
			created, err := mustAccess(t, svc, agentAuthority(t, userID, agentID)).ShareArtifact(ctx, "session", "artifact.html", tt.scope, agentID, "7d")
			if err != nil {
				t.Fatalf("ShareArtifact: %v", err)
			}
			if string(created.Share.Content) != tt.name || viewer.calls != 1 {
				t.Fatalf("content/calls = %q/%d, want %q/1", created.Share.Content, viewer.calls, tt.name)
			}
			wantScope := home.RootPrincipalData
			if tt.rootField == "agent" {
				wantScope = home.RootAgentWorkspace
			}
			if viewer.scope != wantScope || viewer.access != home.RootReadOnly {
				t.Fatalf("scope/access = %v/%v, want %v/%v", viewer.scope, viewer.access, wantScope, home.RootReadOnly)
			}
		})
	}
}

func TestShareArtifactWorkspaceRootFailuresAndGroupSessionsFailClosed(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	mem := memorytest.New()
	userID, agentID := seedShareUser(t, db, "workspace-failure"), "workspace-failure-agent"
	if err := mem.SaveInfo(ctx, memory.SessionInfo{ID: "user-session", UserID: userID, AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("home unavailable")
	viewer := &recordingShareRootOpener{err: want}
	svc := sharepkg.NewService(sqlc.New(db), mem, recally.NewStore(db), t.TempDir(), "http://stella.test", sharepkg.WithHomeWorkspace(viewer), sharepkg.WithAgentAccess(allowAgentRead{}))
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
