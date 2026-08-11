package channel

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/agent"
	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
)

type resolveServiceManager struct{}

func (resolveServiceManager) GetService(string) *agent.Service { return &agent.Service{} }
func (resolveServiceManager) Default() *agent.Service          { return &agent.Service{} }

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
