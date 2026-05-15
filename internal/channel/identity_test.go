package channel

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/CherryHQ/stella/internal/auth"
	"github.com/CherryHQ/stella/internal/config"
	appdb "github.com/CherryHQ/stella/internal/db"
	pkgchannel "github.com/CherryHQ/stella/pkg/channel"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

type testStores struct {
	store     config.Store
	authStore auth.AuthStore
	db        *sql.DB
}

func setupStores(t *testing.T) testStores {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		t.Fatalf("SeedDefaults: %v", err)
	}

	as := appdb.NewAuthStore(db)
	if err := auth.SeedPolicies(context.Background(), as); err != nil {
		t.Fatalf("SeedPolicies: %v", err)
	}

	return testStores{store: store, authStore: as, db: db}
}

func TestResolveUserNoIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	resolved, err := ResolveUser(ctx, ts.authStore, "telegram", "12345")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if resolved.User.ID != 0 {
		t.Errorf("expected User.ID=0 for unlinked user, got %d", resolved.User.ID)
	}
}

func TestResolveUserLinkedIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "alice", hash)
	_, _ = ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "telegram",
		ExternalID: "99999",
		Name:       "Alice",
	})

	resolved, err := ResolveUser(ctx, ts.authStore, "telegram", "99999")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if resolved.User.ID != authUser.ID {
		t.Errorf("User.ID = %d, want %d", resolved.User.ID, authUser.ID)
	}
	if resolved.User.Role != auth.RoleUser {
		t.Errorf("Role = %q, want %q", resolved.User.Role, auth.RoleUser)
	}
}

func TestResolveUserCandidatesFallsBackToLegacyFeishuOpenID(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "feishu-user", hash)
	identity, _ := ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "feishu",
		ExternalID: "ou_legacy",
		Name:       "Feishu User",
	})

	resolved, match, err := ResolveUserCandidates(ctx, ts.authStore, "feishu", []string{"on_stable", "ou_legacy"})
	if err != nil {
		t.Fatalf("ResolveUserCandidates: %v", err)
	}
	if resolved.User.ID != authUser.ID {
		t.Fatalf("User.ID = %d, want %d", resolved.User.ID, authUser.ID)
	}
	if match.Identity.ID != identity.ID || match.Matched != "ou_legacy" {
		t.Fatalf("match = %+v, want legacy identity %+v", match, identity)
	}
}

func TestMaybeCanonicalizeIdentityPromotesFeishuOpenIDToUnionID(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "canon-user", hash)
	identity, _ := ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "feishu",
		ExternalID: "ou_legacy",
	})

	if err := maybeCanonicalizeIdentity(ctx, ts.authStore, "feishu", "on_stable", identityMatch{Identity: identity, Matched: "ou_legacy"}); err != nil {
		t.Fatalf("maybeCanonicalizeIdentity: %v", err)
	}

	got, err := ts.authStore.GetIdentityByPlatform(ctx, "feishu", "on_stable")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform(new): %v", err)
	}
	if got.ID != identity.ID {
		t.Fatalf("updated identity id = %s, want %s", got.ID, identity.ID)
	}
	if _, err := ts.authStore.GetIdentityByPlatform(ctx, "feishu", "ou_legacy"); err == nil {
		t.Fatal("expected legacy feishu open_id identity to be replaced")
	}
}

func TestResolveUserDeactivated(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "inactive", hash)
	authUser.IsActive = false
	_ = ts.authStore.UpdateUser(ctx, authUser)
	_, _ = ts.authStore.CreateIdentity(ctx, auth.Identity{
		UserID:     authUser.ID,
		Platform:   "qq",
		ExternalID: "111",
	})

	_, err := ResolveUser(ctx, ts.authStore, "qq", "111")
	if err == nil {
		t.Error("expected error for deactivated user")
	}
}

func TestResolveAgentUnlinkedUserDenied(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	identity := ResolvedIdentity{}
	chat := ChatContext{Platform: "telegram", IsGroup: false}
	_, err = ResolveAgent(ctx, ts.store, ts.authStore, engine, identity, chat)
	if !errors.Is(err, ErrAgentAccessDenied) {
		t.Fatalf("expected ErrAgentAccessDenied for unlinked user, got: %v", err)
	}
}

func TestResolveAgentFallbackToFirstEnabled(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "testuser", hash)

	identity := ResolvedIdentity{User: authUser}
	chat := ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, ts.authStore, engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "stella" {
		t.Errorf("agentID = %q, want %q", agentID, "stella")
	}
}

func TestResolveAgentGroupAssignment(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	engine, err := auth.NewEngine(ctx, ts.authStore)
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID: "writer", Name: "Writer", Model: "openai/gpt-4", Workspace: "/tmp/writer", Enabled: true,
	})
	_ = ts.store.SetChatAgent(ctx, "telegram", "telegram", "-999", "writer")

	hash, _ := auth.HashPassword("testpass")
	authUser, _ := ts.authStore.CreateUser(ctx, "groupuser", hash)

	identity := ResolvedIdentity{User: authUser}
	chat := ChatContext{Platform: "telegram", ChatID: "-999", IsGroup: true}
	agentID, err := ResolveAgent(ctx, ts.store, ts.authStore, engine, identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	if agentID != "writer" {
		t.Errorf("agentID = %q, want %q", agentID, "writer")
	}
}

func TestTryLinkCodeSuccess(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "bob", hash)

	code := linkCodes.Generate(authUser.ID, "telegram")

	resp, ok := TryLinkCode(ctx, ts.authStore, linkCodes, "/link "+code, "telegram", "67890", "Bob")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message")
	}
	if !strings.Contains(resp, "linked successfully") {
		t.Errorf("unexpected response: %s", resp)
	}

	identity, err := ts.authStore.GetIdentityByPlatform(ctx, "telegram", "67890")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform: %v", err)
	}
	if identity.UserID != authUser.ID {
		t.Errorf("identity.UserID = %d, want %d", identity.UserID, authUser.ID)
	}
}

func TestTryLinkCodeWithCandidatesPrefersStableFeishuID(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "feishu-link", hash)
	code := linkCodes.Generate(authUser.ID, "feishu")

	resp, ok := TryLinkCodeWithCandidates(ctx, ts.authStore, linkCodes, "/link "+code, "feishu", "on_stable", []string{"on_stable", "ou_legacy"}, "Feishu User")
	if !ok {
		t.Fatal("expected TryLinkCodeWithCandidates to handle the message")
	}
	if !strings.Contains(resp, "linked successfully") {
		t.Fatalf("unexpected response: %s", resp)
	}

	identity, err := ts.authStore.GetIdentityByPlatform(ctx, "feishu", "on_stable")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform: %v", err)
	}
	if identity.UserID != authUser.ID {
		t.Fatalf("identity.UserID = %d, want %d", identity.UserID, authUser.ID)
	}
}

func TestTryLinkCodeWrongPlatform(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	hash, _ := auth.HashPassword("testpass1")
	authUser, _ := ts.authStore.CreateUser(ctx, "charlie", hash)

	code := linkCodes.Generate(authUser.ID, "telegram")

	resp, ok := TryLinkCode(ctx, ts.authStore, linkCodes, "/link "+code, "qq", "111", "Charlie")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message")
	}
	if resp == "" {
		t.Error("expected non-empty error response")
	}
}

func TestTryLinkCodeNotACode(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	_, ok := TryLinkCode(ctx, ts.authStore, linkCodes, "Hello, how are you?", "telegram", "111", "Test")
	if ok {
		t.Error("expected TryLinkCode to not handle regular text")
	}
}

func TestTryLinkCodeExpiredOrInvalid(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	resp, ok := TryLinkCode(ctx, ts.authStore, linkCodes, "/link AB12CD", "telegram", "111", "Test")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message (code format matches)")
	}
	if !strings.Contains(resp, "invalid or has expired") {
		t.Fatalf("response = %q", resp)
	}
}

func TestCoordinatorProvisionUserAuthNotConfigured(t *testing.T) {
	coord := &Coordinator{}
	err := coord.ProvisionUser(context.Background(), pkgchannel.ProvisionRequest{})
	if err == nil {
		t.Fatal("expected error when auth is not configured")
	}
	if !strings.Contains(err.Error(), "auth not configured") {
		t.Fatalf("error = %v, want auth-not-configured message", err)
	}
}

func TestCoordinatorProvisionUserRequiresExistingAdmin(t *testing.T) {
	ts := setupStores(t)
	coord := &Coordinator{authStore: ts.authStore}

	err := coord.ProvisionUser(context.Background(), pkgchannel.ProvisionRequest{
		Platform:   "telegram",
		ExternalID: "u-no-admin",
		Name:       "No Admin",
		EmailHint:  "noadmin@example.com",
	})
	if err == nil {
		t.Fatal("expected error when no admin exists")
	}
	if !strings.Contains(err.Error(), "no admin exists yet") {
		t.Fatalf("error = %v, want no-admin message", err)
	}
}

func TestCoordinatorProvisionUserCreatesVaultKeys(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	hash, _ := auth.HashPassword("pw123456")
	adminUser, err := ts.authStore.CreateUser(ctx, "admin", hash)
	if err != nil {
		t.Fatalf("CreateUser(admin): %v", err)
	}
	if err := ts.authStore.UpdateUserRole(ctx, adminUser.ID, auth.RoleAdmin); err != nil {
		t.Fatalf("UpdateUserRole(admin): %v", err)
	}

	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	coord := &Coordinator{
		authStore:      ts.authStore,
		vaultRecipient: identity.Recipient(),
	}

	err = coord.ProvisionUser(ctx, pkgchannel.ProvisionRequest{
		Platform:   "telegram",
		ExternalID: "u-vault",
		Name:       "Vault User",
		EmailHint:  "vault@example.com",
	})
	if err != nil {
		t.Fatalf("ProvisionUser: %v", err)
	}

	ident, err := ts.authStore.GetIdentityByPlatform(ctx, "telegram", "u-vault")
	if err != nil {
		t.Fatalf("GetIdentityByPlatform: %v", err)
	}
	user, err := sqlc.New(ts.db).GetAuthUser(ctx, ident.UserID)
	if err != nil {
		t.Fatalf("GetAuthUser: %v", err)
	}
	if user.AgePublicKey == "" {
		t.Fatal("expected AgePublicKey to be provisioned")
	}
	if user.AgePrivateKey == "" {
		t.Fatal("expected AgePrivateKey to be provisioned")
	}
}

func TestProvisionUserVaultKeysNoRecipientIsNoOp(t *testing.T) {
	ts := setupStores(t)
	if err := provisionUserVaultKeys(context.Background(), ts.authStore, nil, 123); err != nil {
		t.Fatalf("provisionUserVaultKeys(nil recipient): %v", err)
	}
}

func TestProvisionUserVaultKeysReturnsStoreError(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("pw123456")
	user, err := ts.authStore.CreateUser(ctx, "temp", hash)
	if err != nil {
		t.Fatalf("CreateUser(temp): %v", err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}
	if err := ts.db.Close(); err != nil {
		t.Fatalf("Close DB: %v", err)
	}
	if err := provisionUserVaultKeys(ctx, ts.authStore, identity.Recipient(), user.ID); err == nil {
		t.Fatal("expected store error after closing DB")
	}
}
