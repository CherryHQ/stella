package channel

import (
	"context"
	"errors"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/home"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type resolveWorkspaceViewer struct {
	request home.WorkspaceRequest
	view    home.WorkspaceView
	err     error
}

func (v *resolveWorkspaceViewer) WorkspaceView(_ context.Context, req home.WorkspaceRequest) (home.WorkspaceView, error) {
	v.request = req
	return v.view, v.err
}

type resolveServiceManager struct{}

func (resolveServiceManager) GetService(string) *agent.Service { return &agent.Service{} }
func (resolveServiceManager) Default() *agent.Service          { return &agent.Service{} }

type resolveGroup struct{ id string }

func (g resolveGroup) ResolveGroupID(context.Context, string, string, string) (string, error) {
	return g.id, nil
}

func TestCoordinatorResolveUserRootUsesWorkspaceView(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "root@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "sender", "Root User")
	agentID := ts.stellaAgentID(t)

	for _, tt := range []struct {
		name    string
		message pkgchannel.IncomingMessage
		groupID string
		want    home.WorkspaceRequest
	}{
		{name: "DM", message: pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "sender"}, want: home.WorkspaceRequest{UserID: user.ID, AgentID: agentID}},
		{name: "group", message: pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "sender", ChatID: "platform-group", IsGroup: true}, groupID: "canonical-group", want: home.WorkspaceRequest{UserID: user.ID, GroupID: "canonical-group", AgentID: agentID}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			viewer := &resolveWorkspaceViewer{view: home.WorkspaceView{PrincipalRoot: "/supplied/" + tt.name}}
			coord := &Coordinator{serviceManager: resolveServiceManager{}, store: ts.store, auth: ts.oidcStore, agentAccess: agentaccess.NewService(ts.store, ts.authStore), homes: viewer}
			if tt.groupID != "" {
				coord.groupResolver = resolveGroup{id: tt.groupID}
			}
			got, err := coord.ResolveUserRoot(ctx, tt.message)
			if err != nil {
				t.Fatalf("ResolveUserRoot: %v", err)
			}
			if got != viewer.view.PrincipalRoot {
				t.Fatalf("root = %q, want PrincipalRoot %q", got, viewer.view.PrincipalRoot)
			}
			if viewer.request != tt.want {
				t.Fatalf("workspace request = %+v, want %+v", viewer.request, tt.want)
			}
		})
	}
}

func TestCoordinatorResolveUserRootPropagatesWorkspaceError(t *testing.T) {
	ts := setupStores(t)
	user := createTestUser(t, ts.oidcStore, "root-error@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "error-sender", "Root Error")
	want := errors.New("home unavailable")
	coord := &Coordinator{serviceManager: resolveServiceManager{}, store: ts.store, auth: ts.oidcStore, agentAccess: agentaccess.NewService(ts.store, ts.authStore), homes: &resolveWorkspaceViewer{err: want}}
	if _, err := coord.ResolveUserRoot(context.Background(), pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "error-sender"}); !errors.Is(err, want) {
		t.Fatalf("ResolveUserRoot error = %v, want workspace error", err)
	}
}
