package channel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/home"
	"github.com/CherryHQ/stella/pkg/ai"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type resolveServiceManager struct{ lookups *atomic.Int64 }

func (m resolveServiceManager) GetService(string) *agent.Service {
	if m.lookups != nil {
		m.lookups.Add(1)
	}
	return &agent.Service{}
}

func (m resolveServiceManager) Default() *agent.Service {
	if m.lookups != nil {
		m.lookups.Add(1)
	}
	return &agent.Service{}
}

type resolveGroup struct{ id string }

func (g resolveGroup) ResolveGroupID(context.Context, string, string, string) (string, error) {
	return g.id, nil
}

func TestCoordinatorAdmitsKnownAttachmentPrincipals(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "root@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "sender", "Root User")
	agentID := ts.stellaAgentID(t)
	if _, err := ts.db.Exec(ctx, `INSERT INTO channel (id, name, type, agent_id, enabled) VALUES ('typed-home-test', 'Typed Home Test', 'telegram', $1, true)`, agentID); err != nil {
		t.Fatalf("create group channel: %v", err)
	}

	for _, tt := range []struct {
		name    string
		message pkgchannel.IncomingMessage
		groupID string
	}{
		{name: "DM", message: pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "sender"}},
		{name: "group", message: pkgchannel.IncomingMessage{Platform: "telegram", ChannelID: "typed-home-test", SenderID: "sender", ChatID: "platform-group", IsGroup: true}, groupID: "canonical-group"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			coord := &Coordinator{serviceManager: resolveServiceManager{}, store: ts.store, auth: ts.oidcStore, agentAccess: agentaccess.NewService(ts.store, ts.authStore)}
			if tt.groupID != "" {
				coord.groupResolver = resolveGroup{id: tt.groupID}
			}
			err := coord.AdmitAssetSave(ctx, tt.message)
			if err != nil {
				t.Fatalf("AdmitAssetSave: %v", err)
			}
		})
	}
}

func TestPreSessionAttachmentSaveDoesNotStartOrWakeSessionCompute(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "attachment-compute-spy@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "compute-spy", "Compute Spy")
	homeDir := t.TempDir()
	homes, err := home.NewWorkspaceManager(ts.db, homeDir)
	if err != nil {
		t.Fatalf("NewWorkspaceManager: %v", err)
	}
	var computeLookups atomic.Int64
	coord := &Coordinator{
		serviceManager: resolveServiceManager{lookups: &computeLookups},
		store:          ts.store,
		auth:           ts.oidcStore,
		agentAccess:    agentaccess.NewService(ts.store, ts.authStore),
		rootOpener:     homes,
	}
	logical, err := coord.SaveAsset(ctx, pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "compute-spy"}, "photo.jpg", []byte("image"))
	if err != nil {
		t.Fatalf("SaveAsset: %v", err)
	}
	if logical == "" {
		t.Fatal("SaveAsset returned an empty logical path")
	}
	if got := computeLookups.Load(); got != 0 {
		t.Fatalf("Session compute lookups = %d, want 0", got)
	}
}

func TestCoordinatorSaveAssetPublishesEmptyFile(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "empty-attachment@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "telegram", "empty-attachment", "Empty Attachment")
	agentID := ts.stellaAgentID(t)
	homeDir := t.TempDir()
	homes, err := home.NewWorkspaceManager(ts.db, homeDir)
	if err != nil {
		t.Fatalf("NewWorkspaceManager: %v", err)
	}
	coord := &Coordinator{
		serviceManager: resolveServiceManager{},
		store:          ts.store,
		auth:           ts.oidcStore,
		agentAccess:    agentaccess.NewService(ts.store, ts.authStore),
		rootOpener:     homes,
	}
	logical, err := coord.SaveAsset(ctx, pkgchannel.IncomingMessage{Platform: "telegram", SenderID: "empty-attachment"}, "empty.txt", nil)
	if err != nil {
		t.Fatalf("SaveAsset: %v", err)
	}
	const prefix = "$STELLA_ASSETS_DIR/"
	if !strings.HasPrefix(logical, prefix) || strings.TrimPrefix(logical, prefix) == "" {
		t.Fatalf("SaveAsset path = %q, want %s<name>", logical, prefix)
	}
	view, err := homes.WorkspaceView(ctx, home.WorkspaceRequest{UserID: user.ID, AgentID: agentID})
	if err != nil {
		t.Fatalf("WorkspaceView: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(view.DataRoot, "assets", filepath.FromSlash(strings.TrimPrefix(logical, prefix))))
	if err != nil {
		t.Fatalf("read published attachment: %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("published attachment size = %d, want 0", len(data))
	}
}

func TestImmutableChannelFileRejectsPathNotBoundToBytes(t *testing.T) {
	data := []byte("immutable bytes")
	coord := &Coordinator{}
	_, _, _, err := coord.immutableChannelContentWithQueries(t.Context(), nil, "user", "agent", []ai.ContentBlock{
		ai.FileContent{Name: "report.pdf", Path: pkgchannel.ImmutableAssetPath("report.pdf", []byte("different bytes")), Data: data},
	})
	if err == nil || !strings.Contains(err.Error(), "content-addressed path") {
		t.Fatalf("mismatched file path error = %v", err)
	}
}
