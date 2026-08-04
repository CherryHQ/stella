package share_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	agentaccess "github.com/CherryHQ/stella/internal/agent/access"
	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/internal/db/dbtest"
	"github.com/CherryHQ/stella/internal/memory/memorytest"
	"github.com/CherryHQ/stella/internal/recally"
	sharepkg "github.com/CherryHQ/stella/internal/share"
	"github.com/CherryHQ/stella/pkg/db/sqlc"
)

// saveArticle stores a recally article for userID and returns its id.
func saveArticle(t *testing.T, db *pgxpool.Pool, store *recally.Store, home, userID string) string {
	t.Helper()
	acc, err := recally.NewService(store, home).Access(userAuthority(t, userID))
	if err != nil {
		t.Fatalf("recally Access: %v", err)
	}
	saved, err := acc.Save(context.Background(), recally.SaveRequest{URL: "https://example.com/a", Title: "A", Content: "body"})
	if err != nil {
		t.Fatalf("Save article: %v", err)
	}
	return saved.Article.ID
}

// Access rejects an invalid Authority (403) and a valid Authority carrying no
// user (a system agent → 401) before any operation.
func TestShareAccessRejectsInvalidAndSystemAuthority(t *testing.T) {
	db := dbtest.New(t)
	svc := newShareService(t, db)
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

// An owner shares an article, lists it, then revokes it.
func TestShareOwnerRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := recally.NewStore(db)
	home := t.TempDir()
	svc := sharepkg.NewService(sqlc.New(db), memorytest.New(), store, mustAssets(t, home, nil), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
	userID := seedShareUser(t, db, "owner")
	articleID := saveArticle(t, db, store, home, userID)

	acc := mustAccess(t, svc, userAuthority(t, userID))
	created, err := acc.ShareArticle(ctx, articleID, "7d")
	if err != nil {
		t.Fatalf("ShareArticle: %v", err)
	}
	list, err := acc.List(ctx, 10, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list.Shares) != 1 || list.Shares[0].ID != created.Share.ID {
		t.Fatalf("List=%+v, want the one created share", list.Shares)
	}
	if err := acc.Revoke(ctx, created.Share.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if again, err := acc.List(ctx, 10, 0); err != nil || len(again.Shares) != 0 {
		t.Fatalf("post-revoke List=%+v err=%v, want empty", again, err)
	}
}

// A foreign user sees none of the owner's shares and cannot revoke one; the
// user-scoped queries isolate them.
func TestShareForeignUserIsolated(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := recally.NewStore(db)
	home := t.TempDir()
	svc := sharepkg.NewService(sqlc.New(db), memorytest.New(), store, mustAssets(t, home, nil), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
	ownerID := seedShareUser(t, db, "owner")
	foreignID := seedShareUser(t, db, "foreign")
	articleID := saveArticle(t, db, store, home, ownerID)

	created, err := mustAccess(t, svc, userAuthority(t, ownerID)).ShareArticle(ctx, articleID, "7d")
	if err != nil {
		t.Fatalf("owner ShareArticle: %v", err)
	}

	foreign := mustAccess(t, svc, userAuthority(t, foreignID))
	if list, err := foreign.List(ctx, 10, 0); err != nil || len(list.Shares) != 0 {
		t.Fatalf("foreign List=%+v err=%v, want empty", list, err)
	}
	if err := foreign.Revoke(ctx, created.Share.ID); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign Revoke err=%v, want ErrNotFound", err)
	}
}

// An article owned by another user cannot be shared: the uid-scoped article load
// makes it not-found for the foreign caller.
func TestShareArticleForeignArticleHidden(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	store := recally.NewStore(db)
	home := t.TempDir()
	svc := sharepkg.NewService(sqlc.New(db), memorytest.New(), store, mustAssets(t, home, nil), home, "http://stella.test", sharepkg.WithHomeWorkspace(testWorkspaceViewer{root: home}))
	ownerID := seedShareUser(t, db, "owner")
	foreignID := seedShareUser(t, db, "foreign")
	articleID := saveArticle(t, db, store, home, ownerID)

	foreign := mustAccess(t, svc, userAuthority(t, foreignID))
	if _, err := foreign.ShareArticle(ctx, articleID, "7d"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("foreign ShareArticle err=%v, want ErrNotFound", err)
	}
}

// An agent-scoped actor cannot select a different agent's workspace; the Access
// confines it to its bound agent before any filesystem access.
func TestShareAgentCannotShareForeignAgentWorkspace(t *testing.T) {
	ctx := context.Background()
	db := dbtest.New(t)
	svc := newShareService(t, db)
	userID := seedShareUser(t, db, "agent")

	acc := mustAccess(t, svc, agentAuthority(t, userID, "agent-self"))
	if _, err := acc.ShareArtifact(ctx, "session", "ok.html", "", "agent-other", "7d"); !errors.Is(err, authz.ErrForbidden) {
		t.Fatalf("cross-agent ShareArtifact err=%v, want ErrForbidden", err)
	}
}
