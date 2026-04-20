package auth_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vaayne/anna/internal/auth"
	appdb "github.com/vaayne/anna/internal/db"
)

func setupProvisionStore(t *testing.T) auth.AuthStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "provision.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewAuthStore(db)
}

func TestProvisionIdentityUserNew(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_abc123",
		Name:       "Alice",
		EmailHint:  "alice@example.com",
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.ID == 0 {
		t.Fatal("expected non-zero user ID")
	}
	if user.Username != "alice" {
		t.Errorf("username = %q, want %q", user.Username, "alice")
	}
	if user.PasswordHash != "" {
		t.Errorf("password hash should be empty for provisioned users")
	}
	if user.Role != auth.RoleUser {
		t.Errorf("role = %q, want %q", user.Role, auth.RoleUser)
	}
}

func TestProvisionIdentityUserIdempotent(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_dup",
		Name:       "Bob",
		EmailHint:  "bob@example.com",
	}

	u1, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	u2, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if u1.ID != u2.ID {
		t.Errorf("idempotency: got different user IDs %d vs %d", u1.ID, u2.ID)
	}

	users, _ := store.ListUsers(ctx)
	count := 0
	for _, u := range users {
		if strings.HasPrefix(u.Username, "bob") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 user with username prefix 'bob', got %d", count)
	}
}

func TestProvisionIdentityUserUsernameCollision(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	// Two different external IDs that share the same email local-part.
	req1 := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_carol_1",
		Name:       "Carol 1",
		EmailHint:  "carol@example.com",
	}
	req2 := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_carol_2",
		Name:       "Carol 2",
		EmailHint:  "carol@example.com",
	}

	u1, err := auth.ProvisionIdentityUser(ctx, store, req1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	u2, err := auth.ProvisionIdentityUser(ctx, store, req2)
	if err != nil {
		t.Fatalf("second: %v", err)
	}

	if u1.Username != "carol" {
		t.Errorf("u1.Username = %q, want %q", u1.Username, "carol")
	}
	if u2.Username != "carol-2" {
		t.Errorf("u2.Username = %q, want %q", u2.Username, "carol-2")
	}
	if u1.ID == u2.ID {
		t.Error("collision: same user ID for different external IDs")
	}
}

func TestProvisionIdentityUserEmptyEmail(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "on_xyz12345",
		Name:       "Dave",
		EmailHint:  "",
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should fall back to feishu-<first 8 chars of externalID>.
	if user.Username != "feishu-on_xyz12" {
		t.Errorf("username = %q, want %q", user.Username, "feishu-on_xyz12")
	}
}

func TestProvisionIdentityUserShortExternalID(t *testing.T) {
	store := setupProvisionStore(t)
	ctx := context.Background()

	req := auth.ProvisionRequest{
		Platform:   "feishu",
		ExternalID: "short",
		Name:       "Eve",
		EmailHint:  "",
	}

	user, err := auth.ProvisionIdentityUser(ctx, store, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if user.Username != "feishu-short" {
		t.Errorf("username = %q, want %q", user.Username, "feishu-short")
	}
}
