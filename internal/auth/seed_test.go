package auth_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
)

const testOrgID = "test-org-id"

func setupSeedStore(t *testing.T) auth.AuthStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`INSERT OR IGNORE INTO auth_organization (id, name, source) VALUES (?, ?, ?)`, testOrgID, "Test Org", "test")
	if err != nil {
		t.Fatalf("create test org: %v", err)
	}
	return appdb.NewAuthStore(db)
}

func TestSeedPolicies(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	if err := auth.SeedPolicies(ctx, store, testOrgID); err != nil {
		t.Fatalf("SeedPolicies: %v", err)
	}

	policies, err := store.ListEnabledPolicies(ctx)
	if err != nil {
		t.Fatalf("ListEnabledPolicies: %v", err)
	}
	if len(policies) != 9 {
		t.Errorf("expected 9 policies, got %d", len(policies))
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
		"system:user-own-scheduler",
	}
	for _, id := range expectedPolicies {
		if !policyIDs[id] {
			t.Errorf("missing policy %q", id)
		}
	}
}

func TestSeedPolicies_Idempotent(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	if err := auth.SeedPolicies(ctx, store, testOrgID); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := auth.SeedPolicies(ctx, store, testOrgID); err != nil {
		t.Fatalf("second seed should be idempotent: %v", err)
	}

	policies, _ := store.ListEnabledPolicies(ctx)
	if len(policies) != 9 {
		t.Errorf("expected 9 policies after double seed, got %d", len(policies))
	}
}

func TestNewEngine_WithSeededPolicies(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	if err := auth.SeedPolicies(ctx, store, testOrgID); err != nil {
		t.Fatalf("SeedPolicies: %v", err)
	}

	engine, err := auth.NewEngine(ctx, store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if !engine.Can(ctx, auth.AccessRequest{
		Subject:  auth.Subject{UserID: "1", Roles: []string{"admin"}},
		Action:   auth.ActionManage,
		Resource: auth.Resource{Type: auth.ResourceSetting},
	}) {
		t.Error("admin should have full access from seeded policies")
	}

	if engine.Can(ctx, auth.AccessRequest{
		Subject:  auth.Subject{UserID: "2", Roles: []string{"user"}},
		Action:   auth.ActionManage,
		Resource: auth.Resource{Type: auth.ResourceSetting},
	}) {
		t.Error("user should not be able to manage settings")
	}
}
