package recally

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestServiceSaveStoresBodyInDBAndDedups(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	svc := NewService(NewStore(db), home)
	ident := authz.Identity{UserID: testUserID, AgentID: "agent-a", AgentScoped: true}

	first, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/a", Title: "A", Content: "first"})
	if err != nil {
		t.Fatalf("Save first: %v", err)
	}
	// Bodies live in PostgreSQL now; new articles carry no on-disk mirror pointer.
	if !first.Created || first.Article.FilePath != "" {
		t.Fatalf("first=%+v, want created with empty file_path", first)
	}
	body, err := svc.ReadArticleBody(t.Context(), first.Article)
	if err != nil || body != "first" {
		t.Fatalf("ReadArticleBody body=%q err=%v", body, err)
	}
	second, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/a?utm_source=x", CanonicalURL: first.Article.CanonicalURL, Title: "A2", Content: "second"})
	if err != nil {
		t.Fatalf("Save second: %v", err)
	}
	if second.Created || second.Article.ID != first.Article.ID {
		t.Fatalf("second=%+v, want update of same article %s", second, first.Article.ID)
	}
	articles, err := svc.As(ident).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 1 {
		t.Fatalf("ListArticles len=%d err=%v", len(articles), err)
	}
}

func TestServiceReadArticleBodyReadsFromDatabase(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	svc := NewService(NewStore(db), home)
	ident := authz.Identity{UserID: testUserID}

	saved, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/db-body", Title: "DB Body", Content: "from database"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// No on-disk mirror is written; the body must round-trip through PostgreSQL.
	body, err := svc.ReadArticleBody(t.Context(), saved.Article)
	if err != nil || body != "from database" {
		t.Fatalf("ReadArticleBody body=%q err=%v, want database body", body, err)
	}
}

func TestStoreArticleContentInsertIfAbsentDoesNotOverwrite(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewStore(db)

	article, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/content-race", Title: "Content Race", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle: %v", err)
	}
	if err := store.UpsertArticleContent(t.Context(), article.ID, "current body"); err != nil {
		t.Fatalf("UpsertArticleContent seed: %v", err)
	}
	if err := store.InsertArticleContentIfAbsent(t.Context(), article.ID, "legacy body"); err != nil {
		t.Fatalf("InsertArticleContentIfAbsent: %v", err)
	}
	body, ok, err := store.GetArticleContent(t.Context(), article.ID)
	if err != nil || !ok || body != "current body" {
		t.Fatalf("GetArticleContent after insert-if-absent body=%q ok=%v err=%v, want current body", body, ok, err)
	}
	if err := store.UpsertArticleContent(t.Context(), article.ID, "new body"); err != nil {
		t.Fatalf("UpsertArticleContent overwrite: %v", err)
	}
	body, ok, err = store.GetArticleContent(t.Context(), article.ID)
	if err != nil || !ok || body != "new body" {
		t.Fatalf("GetArticleContent after upsert body=%q ok=%v err=%v, want new body", body, ok, err)
	}
}

func TestServiceReadArticleBodyMissingMirrorReturnsNoContent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	store := NewStore(db)
	svc := NewService(store, home)

	article, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/missing-mirror", Title: "Missing Mirror", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle: %v", err)
	}
	article.FilePath = "library/missing.md"
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, article.ID, article.FilePath); err != nil {
		t.Fatalf("UpdateArticleFilePath: %v", err)
	}
	body, err := svc.ReadArticleBody(t.Context(), article)
	if err != nil || body != "" {
		t.Fatalf("ReadArticleBody missing mirror body=%q err=%v, want no content and no error", body, err)
	}
}

func TestServiceReadArticleBodyIgnoresLegacyFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	store := NewStore(db)
	svc := NewService(store, home)

	article, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/legacy", Title: "Legacy", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle: %v", err)
	}
	// A legacy article whose body still lives in a readable on-disk mirror, with
	// the row pointing at it via file_path but no content row yet.
	relPath, _ := writeLegacyMirror(t, home, article.ID, "legacy body")
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, article.ID, relPath); err != nil {
		t.Fatalf("UpdateArticleFilePath: %v", err)
	}
	article.FilePath = relPath

	// Reads are DB-only: even though the file is present and readable, an absent
	// content row yields an empty body and the read must not lazily backfill it.
	body, err := svc.ReadArticleBody(t.Context(), article)
	if err != nil || body != "" {
		t.Fatalf("ReadArticleBody body=%q err=%v, want empty (no file fallback)", body, err)
	}
	if _, ok, err := store.GetArticleContent(t.Context(), article.ID); err != nil || ok {
		t.Fatalf("GetArticleContent after read ok=%v err=%v, want no row (read must not backfill)", ok, err)
	}
}

// TestServiceSaveMetadataOnlyDoesNotStrandLegacyBody guards the backfill-safety
// invariant from the DB-only read migration: a metadata-only update on a legacy
// row that has no content row must NOT insert an empty content row, or the
// startup backfill (file_path set + no content row) would skip it and lose the
// on-disk body forever.
func TestServiceSaveMetadataOnlyDoesNotStrandLegacyBody(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	store := NewStore(db)
	svc := NewService(store, home)
	ident := authz.Identity{UserID: testUserID}

	// store.SaveArticle inserts no content row, so this stands in for a legacy
	// file-only article the backfill has not yet reached.
	article, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/meta-only", Title: "Meta", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle: %v", err)
	}
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, article.ID, "library/meta-only.md"); err != nil {
		t.Fatalf("UpdateArticleFilePath: %v", err)
	}

	// Metadata-only update (no Content) on the same canonical URL.
	res, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/meta-only", CanonicalURL: article.CanonicalURL, Title: "Meta Updated"})
	if err != nil {
		t.Fatalf("Save metadata-only: %v", err)
	}
	if res.Created || res.Article.ID != article.ID {
		t.Fatalf("Save metadata-only res=%+v, want update of %s", res, article.ID)
	}
	if _, ok, err := store.GetArticleContent(t.Context(), article.ID); err != nil || ok {
		t.Fatalf("GetArticleContent after metadata-only save ok=%v err=%v, want no row (invariant: no empty row)", ok, err)
	}
}

// writeLegacyMirror writes a legacy on-disk article mirror (YAML frontmatter
// followed by the body) under home and returns its STELLA_HOME-relative and
// absolute paths. It stands in for the retired FileManager.WriteArticle so the
// startup-backfill and DB-only read tests can still stage pre-migration data.
func writeLegacyMirror(t *testing.T, home, articleID, body string) (relPath, absPath string) {
	t.Helper()
	relPath = filepath.Join("library", articleID+".md")
	absPath = filepath.Join(home, relPath)
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("mkdir legacy mirror: %v", err)
	}
	if err := os.WriteFile(absPath, []byte("---\ntitle: Legacy\n---\n"+body+"\n"), 0o644); err != nil {
		t.Fatalf("write legacy mirror: %v", err)
	}
	return relPath, absPath
}

func TestServiceDeleteArticleCascadesContent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewStore(db)
	svc := NewService(store, t.TempDir())
	ident := authz.Identity{UserID: testUserID}

	saved, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/delete", Title: "Delete", Content: "delete me"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, ok, err := store.GetArticleContent(t.Context(), saved.Article.ID); err != nil || !ok {
		t.Fatalf("GetArticleContent before delete ok=%v err=%v", ok, err)
	}
	if err := store.DeleteArticle(t.Context(), testUserID, saved.Article.ID); err != nil {
		t.Fatalf("DeleteArticle: %v", err)
	}
	if _, ok, err := store.GetArticleContent(t.Context(), saved.Article.ID); err != nil || ok {
		t.Fatalf("GetArticleContent after delete ok=%v err=%v, want missing", ok, err)
	}
}

func TestServiceBackfillMissingContent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	store := NewStore(db)
	svc := NewService(store, home)

	// Legacy article: body lives only in a disk file, no content row yet.
	legacy, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/legacy", Title: "Legacy", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle legacy: %v", err)
	}
	legacyRel, _ := writeLegacyMirror(t, home, legacy.ID, "legacy body")
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, legacy.ID, legacyRel); err != nil {
		t.Fatalf("UpdateArticleFilePath legacy: %v", err)
	}

	// Already-backfilled article: has a stored body; backfill must not touch it.
	kept, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/kept", Title: "Kept", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle kept: %v", err)
	}
	if err := store.UpsertArticleContent(t.Context(), kept.ID, "kept body"); err != nil {
		t.Fatalf("UpsertArticleContent kept: %v", err)
	}
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, kept.ID, "library/kept.md"); err != nil {
		t.Fatalf("UpdateArticleFilePath kept: %v", err)
	}

	// Legacy article whose mirror is gone (e.g. lives on another pod's disk).
	gone, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/gone", Title: "Gone", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle gone: %v", err)
	}
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, gone.ID, "library/gone-missing.md"); err != nil {
		t.Fatalf("UpdateArticleFilePath gone: %v", err)
	}

	scanned, backfilled, missing, err := svc.BackfillMissingContent(t.Context())
	if err != nil {
		t.Fatalf("BackfillMissingContent: %v", err)
	}
	// Only legacy and gone are candidates; kept already has a content row.
	if scanned != 2 || backfilled != 1 || missing != 1 {
		t.Fatalf("scanned=%d backfilled=%d missing=%d, want 2/1/1", scanned, backfilled, missing)
	}

	if body, ok, err := store.GetArticleContent(t.Context(), legacy.ID); err != nil || !ok || body != "legacy body" {
		t.Fatalf("legacy content body=%q ok=%v err=%v, want backfilled", body, ok, err)
	}
	if body, ok, err := store.GetArticleContent(t.Context(), kept.ID); err != nil || !ok || body != "kept body" {
		t.Fatalf("kept content body=%q ok=%v err=%v, want unchanged", body, ok, err)
	}
	if _, ok, err := store.GetArticleContent(t.Context(), gone.ID); err != nil || ok {
		t.Fatalf("gone content ok=%v err=%v, want no row", ok, err)
	}
}

func TestServiceAuthorizedIdentityAndMissingMapping(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	if _, err := svc.As(authz.Identity{}).ListArticles(t.Context(), ArticleFilter{}); !errors.Is(err, authz.ErrUnauthenticated) {
		t.Fatalf("ListArticles unauth err=%v, want ErrUnauthenticated", err)
	}
	if _, err := svc.As(authz.Identity{UserID: testUserID}).GetArticle(t.Context(), "missing"); !errors.Is(err, authz.ErrNotFound) {
		t.Fatalf("GetArticle missing err=%v, want ErrNotFound", err)
	}
	_, err := svc.As(authz.Identity{UserID: testUserID}).Save(t.Context(), SaveRequest{URL: "https://example.com/new"})
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("Save missing content err=%v", err)
	}
}
