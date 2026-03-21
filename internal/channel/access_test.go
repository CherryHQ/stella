package channel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vaayne/anna/internal/auth"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
)

// setupWithEngine extends setupStores to also create a policy engine.
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

	// Create an auth user with "user" role.
	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "alice", hash)

	identity := channel.ResolvedIdentity{
		User: authUser,
	}

	// The default "anna" agent is system-scoped, so any user should access it.
	chat := channel.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := channel.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgentWithAuth: %v", err)
	}
	if agentID != "anna" {
		t.Errorf("agentID = %q, want %q", agentID, "anna")
	}
}

func TestResolveAgentWithAuthRestrictedDenied(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	// Create a restricted agent.
	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "private",
		Name:      "Private",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/private",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	// Create user with "user" role (NOT assigned to the agent).
	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "bob", hash)
	_ = ts.authStore.UpdateUserDefaultAgent(ctx, authUser.ID, "private")
	authUser, _ = ts.authStore.GetUser(ctx, authUser.ID) // reload with default_agent_id

	identity := channel.ResolvedIdentity{
		User: authUser,
	}

	// User's default agent is "private" but they are not assigned.
	chat := channel.ChatContext{Platform: "telegram", IsGroup: false}
	_, err := channel.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err == nil {
		t.Fatal("expected error for unassigned restricted agent")
	}
	if !errors.Is(err, channel.ErrAgentAccessDenied) {
		t.Errorf("expected ErrAgentAccessDenied, got: %v", err)
	}
}

func TestResolveAgentWithAuthRestrictedAllowed(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	// Create a restricted agent.
	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "vip",
		Name:      "VIP",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/vip",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	// Create user and assign to the agent.
	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "charlie", hash)
	_ = ts.authStore.AssignAgent(ctx, authUser.ID, "vip")
	_ = ts.authStore.UpdateUserDefaultAgent(ctx, authUser.ID, "vip")
	authUser, _ = ts.authStore.GetUser(ctx, authUser.ID)

	identity := channel.ResolvedIdentity{
		User: authUser,
	}

	chat := channel.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := channel.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
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

	// Create a restricted agent.
	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "admin-only",
		Name:      "Admin Only",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/admin-only",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	// Create admin user (NOT explicitly assigned to the agent).
	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "adminuser", hash)
	_ = ts.authStore.UpdateUserRole(ctx, authUser.ID, auth.RoleAdmin)
	_ = ts.authStore.UpdateUserDefaultAgent(ctx, authUser.ID, "admin-only")
	authUser, _ = ts.authStore.GetUser(ctx, authUser.ID)

	identity := channel.ResolvedIdentity{
		User: authUser,
	}

	// Admin should access any agent regardless of scope.
	chat := channel.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := channel.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
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

	// Make "anna" restricted so user can't access it.
	_ = ts.store.UpdateAgent(ctx, config.Agent{
		ID:        "anna",
		Name:      "Anna",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/anna",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	// Create a system-scoped agent.
	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "public",
		Name:      "Public",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/public",
		Scope:     config.AgentScopeSystem,
		Enabled:   true,
	})

	// Create user with no default agent, no assignment.
	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "dave", hash)

	identity := channel.ResolvedIdentity{
		User: authUser,
	}

	// Fallback should skip restricted "anna" and return system "public".
	chat := channel.ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := channel.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgentWithAuth: %v", err)
	}
	if agentID != "public" {
		t.Errorf("agentID = %q, want %q", agentID, "public")
	}
}

func TestResolveAgentWithAuthGroupChatDenied(t *testing.T) {
	ts := setupStoresWithEngine(t)
	ctx := context.Background()

	// Create a restricted agent.
	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID:        "group-agent",
		Name:      "Group Agent",
		Model:     "anthropic/claude-sonnet-4-6",
		Workspace: "/tmp/group-agent",
		Scope:     config.AgentScopeRestricted,
		Enabled:   true,
	})

	// Assign agent to a group chat.
	_ = ts.store.SetChatAgent(ctx, "telegram", "-1001234", "group-agent")

	// Create user NOT assigned to the agent.
	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "eve", hash)

	identity := channel.ResolvedIdentity{
		User: authUser,
	}

	// Group chat with restricted agent should be denied.
	chat := channel.ChatContext{Platform: "telegram", ChatID: "-1001234", IsGroup: true}
	_, err := channel.ResolveAgent(ctx, ts.store, ts.authStore, ts.engine, identity, chat)
	if err == nil {
		t.Fatal("expected error for unassigned restricted agent in group chat")
	}
	if !errors.Is(err, channel.ErrAgentAccessDenied) {
		t.Errorf("expected ErrAgentAccessDenied, got: %v", err)
	}
}
