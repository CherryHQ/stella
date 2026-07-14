package recally

import (
	"errors"
	"testing"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
)

func userAuthority(t *testing.T, id string) authz.Authority {
	t.Helper()
	a, err := authz.NewUserAuthority(authz.UserID(id), false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func agentAuthority(t *testing.T, userID, agentID string) authz.Authority {
	t.Helper()
	a, err := agentaccess.WorkerAgentAuthority(userID, agentID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// mustAccess binds one recally Access as the given user; used across the recally
// service tests to reach the Authority-bound library.
func mustAccess(t *testing.T, svc *Service, userID string) *Access {
	t.Helper()
	acc, err := svc.Access(userAuthority(t, userID))
	if err != nil {
		t.Fatalf("Access: %v", err)
	}
	return acc
}

// Access rejects an invalid Authority (403) and a valid Authority carrying no
// user (a system agent → 401) before any operation.
func TestRecallyAccessRejectsInvalidAndSystemAuthority(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	if _, err := svc.Access(authz.Authority{}); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("Access(zero) err=%v, want forbidden", err)
	}
	sysAuth, err := agentaccess.SystemAgentAuthority("test")
	if err != nil {
		t.Fatalf("SystemAgentAuthority: %v", err)
	}
	if _, err := svc.Access(sysAuth); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("Access(system) err=%v, want unauthenticated", err)
	}
}

// The owner can save and read back their own article in one round-trip.
func TestRecallyOwnerSaveGetRoundTrips(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	svc := NewService(NewStore(db), t.TempDir())

	saved, err := mustAccess(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/own", Title: "Own", Content: "body"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := mustAccess(t, svc, testUserID).GetArticle(ctx, saved.Article.ID)
	if err != nil {
		t.Fatalf("GetArticle: %v", err)
	}
	if got.ID != saved.Article.ID {
		t.Fatalf("GetArticle id=%s, want %s", got.ID, saved.Article.ID)
	}
}

// A foreign user cannot read another user's article: the uid-scoped store hides
// it as an opaque not-found, never leaking existence. Writes to a foreign
// article's content (keyed only by article id) are likewise refused because the
// owner-scoped parent load fails first.
func TestRecallyForeignUserCannotRead(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'foreign@example.com')`, otherUserID); err != nil {
		t.Fatalf("insert foreign user: %v", err)
	}
	svc := NewService(NewStore(db), t.TempDir())

	saved, err := mustAccess(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/secret", Title: "Secret", Content: "body"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	foreign := mustAccess(t, svc, otherUserID)
	if _, err := foreign.GetArticle(ctx, saved.Article.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign GetArticle err=%v, want ErrNotFound", err)
	}
	if err := foreign.UpsertArticleContent(ctx, saved.Article.ID, "stolen"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign UpsertArticleContent err=%v, want ErrNotFound", err)
	}
	body, err := mustAccess(t, svc, testUserID).ReadArticleBody(ctx, saved.Article)
	if err != nil {
		t.Fatalf("owner ReadArticleBody: %v", err)
	}
	if body != "body" {
		t.Fatalf("body=%q, want original content", body)
	}
}

func TestRecallyFeedEntryCannotLinkForeignArticle(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.Exec(ctx, `INSERT INTO auth_user (id, email) VALUES ($1, 'foreign-link@example.com')`, otherUserID); err != nil {
		t.Fatalf("insert foreign user: %v", err)
	}
	store := NewStore(db)
	svc := NewService(store, t.TempDir())
	feed, err := store.CreateFeed(ctx, testUserID, "https://example.com/feed.xml", FeedKindRSS, nil, "Mine", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := store.CreateFeedEntry(ctx, feed.ID, "guid-1", "https://example.com/item", "Item")
	if err != nil || entry == nil {
		t.Fatalf("CreateFeedEntry: entry=%v err=%v", entry, err)
	}
	foreignArticle, err := mustAccess(t, svc, otherUserID).Save(ctx, SaveRequest{URL: "https://example.com/foreign", Title: "Foreign", Content: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	articleID := foreignArticle.Article.ID
	if _, err := mustAccess(t, svc, testUserID).UpdateFeedEntry(ctx, feed.ID, entry.ID, EntryStatusSaved, &articleID, ""); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("link foreign article err=%v, want ErrNotFound", err)
	}
}

// A delegated agent has the SAME access as its delegating user (recally is
// user-owned, shared across the user's agents).
func TestRecallyAgentActsAsUser(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := t.Context()
	svc := NewService(NewStore(db), t.TempDir())

	saved, err := mustAccess(t, svc, testUserID).Save(ctx, SaveRequest{URL: "https://example.com/shared", Title: "Shared", Content: "body"})
	if err != nil {
		t.Fatalf("owner Save: %v", err)
	}

	acc, err := svc.Access(agentAuthority(t, testUserID, "agent-x"))
	if err != nil {
		t.Fatalf("agent Access: %v", err)
	}
	got, err := acc.GetArticle(ctx, saved.Article.ID)
	if err != nil {
		t.Fatalf("agent GetArticle: %v", err)
	}
	if got.ID != saved.Article.ID {
		t.Fatalf("agent GetArticle id=%s, want %s", got.ID, saved.Article.ID)
	}
}
