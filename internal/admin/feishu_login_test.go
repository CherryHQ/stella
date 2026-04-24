package admin

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/vaayne/anna/internal/auth"
	appdb "github.com/vaayne/anna/internal/db"
)

func TestBuildFeishuAuthURLUsesAbsoluteRedirectURI(t *testing.T) {
	authURL := buildFeishuAuthURL("cli_xxx", "state123", "https://anna.example.com/api/auth/login/feishu/callback")

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth URL: %v", err)
	}
	if got, want := u.Query().Get("redirect_uri"), "https://anna.example.com/api/auth/login/feishu/callback"; got != want {
		t.Fatalf("redirect_uri = %q, want %q", got, want)
	}
}

func TestFeishuLoginCallbackURLUsesConfiguredOrigin(t *testing.T) {
	if got, want := feishuLoginCallbackURL("https://anna.example.com/"), "https://anna.example.com/api/auth/login/feishu/callback"; got != want {
		t.Fatalf("feishuLoginCallbackURL = %q, want %q", got, want)
	}
}

func TestLoginFlowStoreRejectsWhenFullAfterSweepingExpired(t *testing.T) {
	store := NewLoginFlowStore()
	var firstFlowID string
	for i := range maxLoginFlowEntries {
		flowID, err := store.Create("feishu", "feishu", "/", time.Minute)
		if err != nil {
			t.Fatalf("Create before full: %v", err)
		}
		if i == 0 {
			firstFlowID = flowID
		}
	}
	if _, err := store.Create("feishu", "feishu", "/", time.Minute); !errors.Is(err, errLoginFlowStoreFull) {
		t.Fatalf("Create when full error = %v, want %v", err, errLoginFlowStoreFull)
	}

	store.mu.Lock()
	state := store.flows[firstFlowID]
	state.ExpiresAt = time.Now().Add(-time.Minute)
	store.flows[firstFlowID] = state
	store.mu.Unlock()
	if _, err := store.Create("feishu", "feishu", "/", time.Minute); err != nil {
		t.Fatalf("Create after expiring one flow: %v", err)
	}
}

func TestResolveFeishuUserRejectsUnionOpenIDConflict(t *testing.T) {
	ctx := context.Background()
	store := newFeishuLoginAuthStore(t)

	u1, err := store.CreateUser(ctx, "union-user", "")
	if err != nil {
		t.Fatalf("CreateUser union: %v", err)
	}
	u2, err := store.CreateUser(ctx, "open-user", "")
	if err != nil {
		t.Fatalf("CreateUser open: %v", err)
	}
	if _, err := store.CreateIdentity(ctx, auth.Identity{UserID: u1.ID, Platform: "feishu", ExternalID: "union-1"}); err != nil {
		t.Fatalf("CreateIdentity union: %v", err)
	}
	if _, err := store.CreateIdentity(ctx, auth.Identity{UserID: u2.ID, Platform: "feishu", ExternalID: "open-1"}); err != nil {
		t.Fatalf("CreateIdentity open: %v", err)
	}

	_, _, err = resolveFeishuUser(ctx, store, "union-1", "open-1")
	if !errors.Is(err, errFeishuIdentityConflict) {
		t.Fatalf("resolveFeishuUser error = %v, want %v", err, errFeishuIdentityConflict)
	}
}

func TestCanonicalizeFeishuIdentityDeletesSameUserLegacyOpenID(t *testing.T) {
	ctx := context.Background()
	store := newFeishuLoginAuthStore(t)

	user, err := store.CreateUser(ctx, "feishu-user", "")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if _, err := store.CreateIdentity(ctx, auth.Identity{UserID: user.ID, Platform: "feishu", ExternalID: "union-1"}); err != nil {
		t.Fatalf("CreateIdentity union: %v", err)
	}
	legacy, err := store.CreateIdentity(ctx, auth.Identity{UserID: user.ID, Platform: "feishu", ExternalID: "open-1"})
	if err != nil {
		t.Fatalf("CreateIdentity open: %v", err)
	}

	resolved, match, err := resolveFeishuUser(ctx, store, "union-1", "open-1")
	if err != nil {
		t.Fatalf("resolveFeishuUser: %v", err)
	}
	if resolved.User.ID != user.ID {
		t.Fatalf("resolved user ID = %d, want %d", resolved.User.ID, user.ID)
	}
	if match.Identity.ID != legacy.ID {
		t.Fatalf("match identity ID = %d, want legacy %d", match.Identity.ID, legacy.ID)
	}
	if err := canonicalizeFeishuIdentity(ctx, store, "union-1", match); err != nil {
		t.Fatalf("canonicalizeFeishuIdentity: %v", err)
	}
	if _, err := store.GetIdentityByPlatform(ctx, "feishu", "open-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy open_id lookup error = %v, want sql.ErrNoRows", err)
	}
}

func newFeishuLoginAuthStore(t *testing.T) auth.AuthStore {
	t.Helper()
	db, err := appdb.OpenDB(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return appdb.NewAuthStore(db)
}
