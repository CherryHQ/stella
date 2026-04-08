package chatroute_test

import (
	"context"
	"testing"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/chatroute"
	"github.com/vaayne/anna/internal/config"
)

type testStoresWithEngine struct {
	testStores
	engine *auth.PolicyEngine
}

func setupStoresWithEngine(t *testing.T) testStoresWithEngine {
	t.Helper()
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	return testStoresWithEngine{testStores: ts, engine: engine}
}

func TestResolveAgentWithAuthSystemAgent(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "alice", hash)

	identity := chatroute.ResolvedIdentity{User: authUser}

	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgentWithAuth: %v", err)
	}
	if agentID != "anna" {
		t.Errorf("agentID = %q, want %q", agentID, "anna")
	}
}

func TestResolveAgentWithAuthRestrictedFallback(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "private",
		Name:      "Private",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/private",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "bob", hash)
	_ = ts.authStore.UpdateUserDefaultAgent(ctx, authUser.ID, "private")
	authUser, _ = ts.authStore.GetUser(ctx, authUser.ID)

	identity := chatroute.ResolvedIdentity{User: authUser}

	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if agentID != "anna" {
		t.Errorf("agentID = %q, want fallback to %q", agentID, "anna")
	}
}

func TestResolveAgentWithAuthRestrictedAllowed(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "vip",
		Name:      "VIP",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/vip",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "charlie", hash)
	_ = ts.authStore.AssignAgent(ctx, authUser.ID, "vip")
	_ = ts.authStore.UpdateUserDefaultAgent(ctx, authUser.ID, "vip")
	authUser, _ = ts.authStore.GetUser(ctx, authUser.ID)

	identity := chatroute.ResolvedIdentity{User: authUser}

	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "vip" {
		t.Errorf("agentID = %q, want %q", agentID, "vip")
	}
}

func TestResolveAgentWithAuthAdminAccessAll(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "admin-only",
		Name:      "Admin Only",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/admin-only",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "adminuser", hash)
	_ = ts.authStore.UpdateUserRole(ctx, authUser.ID, auth.RoleAdmin)
	_ = ts.authStore.UpdateUserDefaultAgent(ctx, authUser.ID, "admin-only")
	authUser, _ = ts.authStore.GetUser(ctx, authUser.ID)

	identity := chatroute.ResolvedIdentity{User: authUser}

	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "admin-only" {
		t.Errorf("agentID = %q, want %q", agentID, "admin-only")
	}
}

func TestResolveAgentWithAuthFallbackFiltered(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	_ = ts.store.UpdateAgent(ctx, config.Agent{
		ID:        "anna",
		Name:      "Anna",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/anna",
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

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "dave", hash)

	identity := chatroute.ResolvedIdentity{User: authUser}

	chat := chatroute.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgentWithAuth: %v", err)
	}
	if agentID != "public" {
		t.Errorf("agentID = %q, want %q", agentID, "public")
	}
}

func TestResolveAgentWithAuthGroupChatFallback(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "group-agent",
		Name:      "Group Agent",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/group-agent",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	_ = ts.store.SetChatAgent(ctx, "telegram", "-1001234", "group-agent")

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "eve", hash)

	identity := chatroute.ResolvedIdentity{User: authUser}

	chat := chatroute.ChatContext{Platform: "telegram", ChatID: "-1001234", IsGroup: true}
	agentID, err := chatroute.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("expected fallback, got error: %v", err)
	}
	if agentID != "anna" {
		t.Errorf("agentID = %q, want fallback to %q", agentID, "anna")
	}
}
