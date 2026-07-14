package auth_test

import (
	"context"
	"testing"

	"github.com/CherryHQ/stella/internal/auth"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
)

func setupSeedStore(t *testing.T) auth.AuthStore {
	t.Helper()
	db := dbtest.New(t)
	return appdb.NewAuthStore(db)
}

func TestBuiltinPolicies(t *testing.T) {
	policies := auth.BuiltinPolicies()
	if len(policies) != 6 {
		t.Errorf("expected 6 builtin policies, got %d", len(policies))
	}

	policyIDs := make(map[string]bool)
	for _, p := range policies {
		policyIDs[p.ID] = true
		if !p.IsSystem {
			t.Errorf("builtin policy %q should have IsSystem=true", p.ID)
		}
	}
	expectedPolicies := []string{
		"system:admin-full-access",
		"system:user-own-sessions",
		"system:user-own-data",
		"system:user-own-skills",
		"system:user-own-profile",
		"system:user-own-scheduler",
	}
	for _, id := range expectedPolicies {
		if !policyIDs[id] {
			t.Errorf("missing policy %q", id)
		}
	}
}

func TestListEnabledPolicies(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	policies, err := store.ListEnabledPolicies(ctx)
	if err != nil {
		t.Fatalf("ListEnabledPolicies: %v", err)
	}
	if len(policies) != 6 {
		t.Errorf("expected 6 builtin policies, got %d", len(policies))
	}
}

func TestNewEngine_WithBuiltinPolicies(t *testing.T) {
	store := setupSeedStore(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, store)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	if !engine.Can(ctx, auth.AccessRequest{
		Subject:  auth.Subject{UserID: "1", Roles: []string{"admin"}},
		Action:   auth.ActionManage,
		Resource: auth.Resource{Type: auth.ResourceSetting},
	}) {
		t.Error("admin should have full access from builtin policies")
	}

	if engine.Can(ctx, auth.AccessRequest{
		Subject:  auth.Subject{UserID: "2", Roles: []string{"user"}},
		Action:   auth.ActionManage,
		Resource: auth.Resource{Type: auth.ResourceSetting},
	}) {
		t.Error("user should not be able to manage settings")
	}
}
