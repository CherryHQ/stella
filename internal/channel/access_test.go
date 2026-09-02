package channel

import (
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	"github.com/CherryHQ/stella/internal/platform/config"
)

type testStoresWithEngine struct {
	testStores
	access *agentaccess.Service
}

func setupStoresWithEngine(t *testing.T) testStoresWithEngine {
	t.Helper()
	ts := setupStores(t)

	return testStoresWithEngine{testStores: ts, access: agentaccess.NewService(ts.store, ts.authStore)}
}

func TestResolveAgentWithAuthSystemAgent(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()

	authUser := createTestUser(t, ts.oidcStore, "alice@example.com")

	identity := ResolvedIdentity{User: authUser}

	chat := ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgentWithAuth: %v", err)
	}
	stellaID := ts.stellaAgentID(t)
	if agentID != stellaID {
		t.Errorf("agentID = %q, want %q", agentID, stellaID)
	}
}

func TestResolveAgentWithAuthRestrictedFallback(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "private",
		Name:      "Private",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/private",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	authUser := createTestUser(t, ts.oidcStore, "bob@example.com")
	_ = ts.oidcStore.UpdateUserDefaultAgent(ctx, authUser.ID, "private")
	authUser, _ = ts.oidcStore.GetUser(ctx, authUser.ID)

	identity := ResolvedIdentity{User: authUser}

	chat := ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, identity, chat)
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	stellaID := ts.stellaAgentID(t)
	if agentID != stellaID {
		t.Errorf("agentID = %q, want fallback to %q", agentID, stellaID)
	}
}

func TestResolveAgentWithAuthRestrictedAllowed(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "vip",
		Name:      "VIP",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/vip",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	authUser := createTestUser(t, ts.oidcStore, "charlie@example.com")
	_ = ts.authStore.AssignAgent(ctx, authUser.ID, "vip")
	_ = ts.oidcStore.UpdateUserDefaultAgent(ctx, authUser.ID, "vip")
	authUser, _ = ts.oidcStore.GetUser(ctx, authUser.ID)

	identity := ResolvedIdentity{User: authUser}

	chat := ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "vip" {
		t.Errorf("agentID = %q, want %q", agentID, "vip")
	}
}

func TestResolveAgentWithAuthFallbackFiltered(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()

	stellaID := ts.stellaAgentID(t)
	_ = ts.store.UpdateAgent(ctx, config.Agent{
		ID:        stellaID,
		Name:      "Stella",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/stella",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "public",
		Name:      "Public",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/public",
		Scope:     config.AgentScopeSystem,
		Enabled:   true,
	})

	authUser := createTestUser(t, ts.oidcStore, "dave@example.com")

	identity := ResolvedIdentity{User: authUser}

	chat := ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgentWithAuth: %v", err)
	}
	if agentID != "public" {
		t.Errorf("agentID = %q, want %q", agentID, "public")
	}
}

func TestResolveAgentWithAuthGroupChatFallback(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "group-agent",
		Name:      "Group Agent",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/group-agent",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	_ = ts.store.SetChatAgent(ctx, "telegram", "telegram", "-1001234", "group-agent")

	authUser := createTestUser(t, ts.oidcStore, "eve@example.com")

	identity := ResolvedIdentity{User: authUser}

	chat := ChatContext{Platform: "telegram", ChatID: "-1001234", IsGroup: true}
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, identity, chat)
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	stellaID2 := ts.stellaAgentID(t)
	if agentID != stellaID2 {
		t.Errorf("agentID = %q, want fallback to %q", agentID, stellaID2)
	}
}

func TestResolveAgentDedicatedChannelBypassesAgentAssignment(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "dedicated",
		Name:      "Dedicated",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/dedicated",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})
	if err := ts.store.UpsertChannel(ctx, config.Channel{
		ID:      "telegram-support",
		Type:    "telegram",
		AgentID: "dedicated",
		Enabled: true,
		Config:  `{"token":"tg-token"}`,
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	authUser := createTestUser(t, ts.oidcStore, "frank@example.com")
	identity := ResolvedIdentity{User: authUser}

	chat := ChatContext{Platform: "telegram", ChannelID: "telegram-support", ChatID: "123", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "dedicated" {
		t.Errorf("agentID = %q, want %q", agentID, "dedicated")
	}
}

func TestResolveAgentExplicitUnboundChannelUsesLinkedUserFallback(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := ts.ctx()
	if err := ts.store.UpsertChannel(ctx, config.Channel{
		ID:      "telegram-unbound",
		Type:    "telegram",
		Enabled: true,
		Config:  `{"token":"tg-token"}`,
	}); err != nil {
		t.Fatalf("UpsertChannel: %v", err)
	}

	authUser := createTestUser(t, ts.oidcStore, "unbound@example.com")
	want := ts.stellaAgentID(t)
	agentID, err := ResolveAgent(ctx, ts.store, ts.access, ResolvedIdentity{User: authUser}, ChatContext{
		Platform: "telegram", ChannelID: "telegram-unbound", ChatID: "123",
	})
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != want {
		t.Fatalf("agentID = %q, want fallback %q", agentID, want)
	}
}
