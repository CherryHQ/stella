package auth_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vaayne/anna/internal/auth"
	appdb "github.com/vaayne/anna/internal/db"
)

func setupSeedStore(t *testing.T) auth.AuthStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewAuthStore(db)
}

func TestSeedRolesAndPolicies(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	if err := auth.SeedRolesAndPolicies(ctx, store); err != nil {
		t.Fatalf("SeedRolesAndPolicies: %v", err)
	}

	// Check roles.
	roles, err := store.ListRoles(ctx)
	if err != nil {
		t.Fatalf("ListRoles: %v", err)
	}
	if len(roles) != 2 {
		t.Errorf("expected 2 roles, got %d", len(roles))
	}

	roleIDs := make(map[string]bool)
	for _, r := range roles {
		roleIDs[r.ID] = true
		if !r.IsSystem {
			t.Errorf("role %q should be system", r.ID)
		}
	}
	if !roleIDs["admin"] {
		t.Error("missing admin role")
	}
	if !roleIDs["user"] {
		t.Error("missing user role")
	}

	// Check policies.
	policies, err := store.ListEnabledPolicies(ctx)
	if err != nil {
		t.Fatalf("ListEnabledPolicies: %v", err)
	}
	if len(policies) != 8 {
		t.Errorf("expected 8 policies, got %d", len(policies))
	}

	policyIDs := make(map[string]bool)
	for _, p := range policies {
		policyIDs[p.ID] = true
	}
	expectedPolicies := []string{
		"system:admin-full-access",
		"system:user-system-agents",
		"system:user-assigned-agents",
		"system:user-own-sessions",
		"system:user-own-data",
		"system:user-own-skills",
		"system:user-own-profile",
		"system:user-view-agents-list",
	}
	for _, id := range expectedPolicies {
		if !policyIDs[id] {
			t.Errorf("missing policy %q", id)
		}
	}
}

func TestSeedRolesAndPolicies_Idempotent(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	// Seed twice — should not error.
	if err := auth.SeedRolesAndPolicies(ctx, store); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := auth.SeedRolesAndPolicies(ctx, store); err != nil {
		t.Fatalf("second seed should be idempotent: %v", err)
	}

	// Still only 2 roles and 8 policies.
	roles, _ := store.ListRoles(ctx)
	if len(roles) != 2 {
		t.Errorf("expected 2 roles after double seed, got %d", len(roles))
	}
	policies, _ := store.ListEnabledPolicies(ctx)
	if len(policies) != 8 {
		t.Errorf("expected 8 policies after double seed, got %d", len(policies))
	}
}

func TestNewEngine_WithSeededPolicies(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	if err := auth.SeedRolesAndPolicies(ctx, store); err != nil {
		t.Fatalf("SeedRolesAndPolicies: %v", err)
	}

	engine, err := auth.NewEngine(ctx, store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Admin can manage anything.
	if !engine.Can(ctx, auth.AccessRequest{
		Subject:  auth.Subject{UserID: 1, Roles: []string{"admin"}},
		Action:   auth.ActionManage,
		Resource: auth.Resource{Type: auth.ResourceSetting},
	}) {
		t.Error("admin should have full access from seeded policies")
	}

	// Regular user cannot manage settings.
	if engine.Can(ctx, auth.AccessRequest{
		Subject:  auth.Subject{UserID: 2, Roles: []string{"user"}},
		Action:   auth.ActionManage,
		Resource: auth.Resource{Type: auth.ResourceSetting},
	}) {
		t.Error("user should not be able to manage settings")
	}
}
