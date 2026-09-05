package channel

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/google/uuid"

	cfgstore "github.com/CherryHQ/stella/cmd/stellad/store"
	"github.com/CherryHQ/stella/internal/auth"
	agentaccess "github.com/CherryHQ/stella/internal/core/access"
	appdb "github.com/CherryHQ/stella/internal/db"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/platform/config"
)

func TestMain(m *testing.M) { dbtest.Main(m) }

type testStores struct {
	store     config.Store
	authStore *appdb.AuthStore // policy/token store
	oidcStore *appdb.OIDCStore // user/identity/session store
	db        *pgxpool.Pool
}

func (ts testStores) ctx() context.Context {
	return context.Background()
}

func (ts testStores) stellaAgentID(t *testing.T) string {
	t.Helper()
	agents, err := ts.store.ListAgents(ts.ctx())
	if err != nil {
		t.Fatalf("stellaAgentID: %v", err)
	}
	for _, a := range agents {
		if a.Name == "Stella" {
			return a.ID
		}
	}
	t.Fatal("stellaAgentID: no Stella agent found")
	return ""
}

func setupStores(t *testing.T) testStores {
	t.Helper()
	db := dbtest.New(t)

	store := cfgstore.NewDBStore(db)
	ctx := context.Background()
	if err := store.Seed(ctx); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	as := appdb.NewAuthStore(db)
	oidcStore := appdb.NewOIDCStore(db)

	return testStores{store: store, authStore: as, oidcStore: oidcStore, db: db}
}

// createTestUser creates a user in the OIDC store for tests.
func createTestUser(t *testing.T, store *appdb.OIDCStore, email string) auth.User {
	t.Helper()
	ctx := context.Background()
	u, err := store.CreateUser(ctx, auth.User{
		ID:    uuid.NewString(),
		Email: email,
		Name:  email,
	})
	if err != nil {
		t.Fatalf("CreateUser(%q): %v", email, err)
	}
	return u
}

// createTestIdentity creates a channel identity in the OIDC store for tests.
func createTestLoginIdentity(t *testing.T, store *appdb.OIDCStore, userID, provider, subject, email, name string) auth.LoginIdentity {
	t.Helper()
	ctx := context.Background()
	identity, err := store.CreateLoginIdentity(ctx, auth.LoginIdentity{
		ID:              uuid.NewString(),
		UserID:          userID,
		Provider:        provider,
		ProviderSubject: subject,
		Email:           email,
		Name:            name,
	})
	if err != nil {
		t.Fatalf("CreateLoginIdentity(%q/%q): %v", provider, subject, err)
	}
	return identity
}

func createTestIdentity(t *testing.T, store *appdb.OIDCStore, userID, platform, externalID, name string) auth.ChannelIdentity {
	t.Helper()
	ctx := context.Background()
	ci, err := store.CreateChannelIdentity(ctx, auth.ChannelIdentity{
		ID:         uuid.NewString(),
		UserID:     userID,
		Platform:   platform,
		ExternalID: externalID,
		Name:       name,
	})
	if err != nil {
		t.Fatalf("CreateChannelIdentity(%q/%q): %v", platform, externalID, err)
	}
	return ci
}

func TestResolveUserNoIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	resolved, err := ResolveUser(ctx, ts.oidcStore, "telegram", "12345")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if resolved.User.ID != "" {
		t.Errorf("expected empty User.ID for unlinked user, got %q", resolved.User.ID)
	}
}

func TestResolveUserLinkedIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	authUser := createTestUser(t, ts.oidcStore, "alice@example.com")
	createTestIdentity(t, ts.oidcStore, authUser.ID, "telegram", "99999", "Alice")

	resolved, err := ResolveUser(ctx, ts.oidcStore, "telegram", "99999")
	if err != nil {
		t.Fatalf("ResolveUser: %v", err)
	}
	if resolved.User.ID != authUser.ID {
		t.Errorf("User.ID = %q, want %q", resolved.User.ID, authUser.ID)
	}
}

func TestResolveUserCandidatesFallsBackToLegacyFeishuOpenID(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	authUser := createTestUser(t, ts.oidcStore, "feishu-user@example.com")
	identity := createTestIdentity(t, ts.oidcStore, authUser.ID, "feishu", "ou_legacy", "Feishu User")

	resolved, match, err := ResolveUserCandidates(ctx, ts.oidcStore, "feishu", []string{"on_stable", "ou_legacy"})
	if err != nil {
		t.Fatalf("ResolveUserCandidates: %v", err)
	}
	if resolved.User.ID != authUser.ID {
		t.Fatalf("User.ID = %q, want %q", resolved.User.ID, authUser.ID)
	}
	if match.Identity.ID != identity.ID || match.Matched != "ou_legacy" {
		t.Fatalf("match = %+v, want legacy identity %+v", match, identity)
	}
}

func TestResolveUserCandidatesLinksFeishuOAuthIdentity(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	authUser := createTestUser(t, ts.oidcStore, "feishu-login@example.com")
	loginIdentity := createTestLoginIdentity(t, ts.oidcStore, authUser.ID, "feishu", "on_stable", "feishu-login@example.com", "Feishu User")

	resolved, match, err := ResolveUserCandidates(ctx, ts.oidcStore, "feishu", []string{"on_stable", "ou_legacy"})
	if err != nil {
		t.Fatalf("ResolveUserCandidates: %v", err)
	}
	if resolved.User.ID != authUser.ID {
		t.Fatalf("User.ID = %q, want %q", resolved.User.ID, authUser.ID)
	}
	if match.Matched != "on_stable" || match.Identity.UserID != authUser.ID {
		t.Fatalf("match = %+v", match)
	}
	if loginIdentity.UserID != authUser.ID {
		t.Fatalf("login identity user = %q, want %q", loginIdentity.UserID, authUser.ID)
	}

	channelIdentity, err := ts.oidcStore.GetChannelIdentityByPlatform(ctx, "feishu", "on_stable")
	if err != nil {
		t.Fatalf("GetChannelIdentityByPlatform: %v", err)
	}
	if channelIdentity.UserID != authUser.ID {
		t.Fatalf("channel identity user = %q, want %q", channelIdentity.UserID, authUser.ID)
	}
}

func TestMaybeCanonicalizeIdentityPromotesFeishuOpenIDToUnionID(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()

	authUser := createTestUser(t, ts.oidcStore, "canon-user@example.com")
	identity := createTestIdentity(t, ts.oidcStore, authUser.ID, "feishu", "ou_legacy", "")

	if err := maybeCanonicalizeIdentity(ctx, ts.oidcStore, "feishu", "on_stable", identityMatch{Identity: identity, Matched: "ou_legacy"}); err != nil {
		t.Fatalf("maybeCanonicalizeIdentity: %v", err)
	}

	got, err := ts.oidcStore.GetChannelIdentityByPlatform(ctx, "feishu", "on_stable")
	if err != nil {
		t.Fatalf("GetChannelIdentityByPlatform(new): %v", err)
	}
	if got.ID != identity.ID {
		t.Fatalf("updated identity id = %s, want %s", got.ID, identity.ID)
	}
	if _, err := ts.oidcStore.GetChannelIdentityByPlatform(ctx, "feishu", "ou_legacy"); err == nil {
		t.Fatal("expected legacy feishu open_id identity to be replaced")
	}
}

func TestResolveUserDeactivatedChannelIdentityDenied(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "inactive-channel@example.com")
	createTestIdentity(t, ts.oidcStore, user.ID, "qq", "111", "Inactive")
	if err := ts.oidcStore.UpdateUserActive(ctx, user.ID, false); err != nil {
		t.Fatalf("UpdateUserActive: %v", err)
	}

	_, err := ResolveUser(ctx, ts.oidcStore, "qq", "111")
	if !errors.Is(err, ErrAgentAccessDenied) {
		t.Fatalf("ResolveUser error = %v, want authorization denial", err)
	}
}

func TestResolveUserDeactivatedLoginIdentityDeniedWithoutLinking(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	user := createTestUser(t, ts.oidcStore, "inactive-login@example.com")
	createTestLoginIdentity(t, ts.oidcStore, user.ID, "feishu", "on_inactive", user.Email, "Inactive")
	if err := ts.oidcStore.UpdateUserActive(ctx, user.ID, false); err != nil {
		t.Fatalf("UpdateUserActive: %v", err)
	}

	_, err := ResolveUser(ctx, ts.oidcStore, "feishu", "on_inactive")
	if !errors.Is(err, ErrAgentAccessDenied) {
		t.Fatalf("ResolveUser error = %v, want authorization denial", err)
	}
	if _, err := ts.oidcStore.GetChannelIdentityByPlatform(ctx, "feishu", "on_inactive"); !isNotFound(err) {
		t.Fatalf("inactive login identity created channel link: %v", err)
	}
}

func TestResolveAgentUnlinkedUserDenied(t *testing.T) {
	ts := setupStores(t)
	ctx := ts.ctx()

	identity := ResolvedIdentity{}
	chat := ChatContext{Platform: "telegram", IsGroup: false}
	_, err := ResolveAgent(ctx, ts.store, agentaccess.NewService(ts.store, ts.authStore), identity, chat)
	if !errors.Is(err, ErrAgentAccessDenied) {
		t.Fatalf("expected ErrAgentAccessDenied for unlinked user, got: %v", err)
	}
}

func TestResolveAgentFallbackToFirstEnabled(t *testing.T) {
	ts := setupStores(t)
	ctx := ts.ctx()

	authUser := createTestUser(t, ts.oidcStore, "testuser@example.com")

	identity := ResolvedIdentity{User: authUser}
	chat := ChatContext{Platform: "telegram", IsGroup: false}
	agentID, err := ResolveAgent(ctx, ts.store, agentaccess.NewService(ts.store, ts.authStore), identity, chat)
	if err != nil {
		t.Fatalf("ResolveAgent: %v", err)
	}
	stellaID := ts.stellaAgentID(t)
	if agentID != stellaID {
		t.Errorf("agentID = %q, want %q", agentID, stellaID)
	}
}

func TestResolveAgentGroupAssignment(t *testing.T) {
	ts := setupStores(t)
	ctx := ts.ctx()

	_ = ts.store.CreateAgent(ctx, config.Agent{
		ID: "writer", Name: "Writer", Model: "openai/gpt-4", Workspace: "/tmp/writer", Enabled: true,
	})
	_ = ts.store.SetChatAgent(ctx, "telegram", "telegram", "-999", "writer")

	authUser := createTestUser(t, ts.oidcStore, "groupuser@example.com")

	identity := ResolvedIdentity{User: authUser}
	chat := ChatContext{Platform: "telegram", ChatID: "-999", IsGroup: true}
	agentID, err := ResolveAgent(ctx, ts.store, agentaccess.NewService(ts.store, ts.authStore), identity, chat)
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

	authUser := createTestUser(t, ts.oidcStore, "bob@example.com")

	code := linkCodes.Generate(authUser.ID, "telegram")

	resp, ok := TryLinkCode(ctx, ts.oidcStore, linkCodes, "/link "+code, "telegram", "67890", "Bob")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message")
	}
	if !strings.Contains(resp, "linked successfully") {
		t.Errorf("unexpected response: %s", resp)
	}

	identity, err := ts.oidcStore.GetChannelIdentityByPlatform(ctx, "telegram", "67890")
	if err != nil {
		t.Fatalf("GetChannelIdentityByPlatform: %v", err)
	}
	if identity.UserID != authUser.ID {
		t.Errorf("identity.UserID = %q, want %q", identity.UserID, authUser.ID)
	}
}

func TestTryLinkCodeWithCandidatesPrefersStableFeishuID(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	authUser := createTestUser(t, ts.oidcStore, "feishu-link@example.com")
	code := linkCodes.Generate(authUser.ID, "feishu")

	resp, ok := TryLinkCodeWithCandidates(ctx, ts.oidcStore, linkCodes, "/link "+code, "feishu", "on_stable", []string{"on_stable", "ou_legacy"}, "Feishu User")
	if !ok {
		t.Fatal("expected TryLinkCodeWithCandidates to handle the message")
	}
	if !strings.Contains(resp, "linked successfully") {
		t.Fatalf("unexpected response: %s", resp)
	}

	identity, err := ts.oidcStore.GetChannelIdentityByPlatform(ctx, "feishu", "on_stable")
	if err != nil {
		t.Fatalf("GetChannelIdentityByPlatform: %v", err)
	}
	if identity.UserID != authUser.ID {
		t.Fatalf("identity.UserID = %q, want %q", identity.UserID, authUser.ID)
	}
}

func TestTryLinkCodeWrongPlatform(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	authUser := createTestUser(t, ts.oidcStore, "charlie@example.com")

	code := linkCodes.Generate(authUser.ID, "telegram")

	resp, ok := TryLinkCode(ctx, ts.oidcStore, linkCodes, "/link "+code, "qq", "111", "Charlie")
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

	_, ok := TryLinkCode(ctx, ts.oidcStore, linkCodes, "Hello, how are you?", "telegram", "111", "Test")
	if ok {
		t.Error("expected TryLinkCode to not handle regular text")
	}
}

func TestTryLinkCodeExpiredOrInvalid(t *testing.T) {
	ts := setupStores(t)
	ctx := context.Background()
	linkCodes := auth.NewLinkCodeStore()

	resp, ok := TryLinkCode(ctx, ts.oidcStore, linkCodes, "/link AB12CD", "telegram", "111", "Test")
	if !ok {
		t.Fatal("expected TryLinkCode to handle the message (code format matches)")
	}
	if !strings.Contains(resp, "invalid or has expired") {
		t.Fatalf("response = %q", resp)
	}
}
