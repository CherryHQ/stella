package recally

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
)

func TestServiceSaveWritesFilesAndDedups(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	svc := NewService(NewStore(db), NewFileManager(home), home)
	ident := authz.Identity{UserID: testUserID, AgentID: "agent-a", AgentScoped: true}

	first, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/a", Title: "A", Content: "first"})
	if err != nil {
		t.Fatalf("Save first: %v", err)
	}
	if !first.Created || first.Article.FilePath == "" {
		t.Fatalf("first=%+v, want created with file", first)
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

func TestServiceReadArticleBodyUsesDatabaseWhenMirrorIsMissing(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	svc := NewService(NewStore(db), NewFileManager(home), home)
	ident := authz.Identity{UserID: testUserID}

	saved, err := svc.As(ident).Save(t.Context(), SaveRequest{URL: "https://example.com/db-body", Title: "DB Body", Content: "from database"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Remove(articleAbsPath(home, saved.Article)); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	body, err := svc.ReadArticleBody(t.Context(), saved.Article)
	if err != nil || body != "from database" {
		t.Fatalf("ReadArticleBody body=%q err=%v, want database body", body, err)
	}
}

func TestServiceReadArticleBodyBackfillsLegacyFile(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	store := NewStore(db)
	files := NewFileManager(home)
	svc := NewService(store, files, home)

	article, _, err := store.SaveArticle(t.Context(), testUserID, SaveRequest{URL: "https://example.com/legacy", Title: "Legacy", Content: "ignored"})
	if err != nil {
		t.Fatalf("SaveArticle: %v", err)
	}
	path := files.ArticlePath(testUserID, article.ID, article.Title, article.SavedAt)
	if err := files.WriteArticle(path, article, "legacy body"); err != nil {
		t.Fatalf("WriteArticle: %v", err)
	}
	relPath := files.RelativePath(path)
	if err := store.UpdateArticleFilePath(t.Context(), testUserID, article.ID, relPath); err != nil {
		t.Fatalf("UpdateArticleFilePath: %v", err)
	}
	article.FilePath = relPath

	body, err := svc.ReadArticleBody(t.Context(), article)
	if err != nil || body != "legacy body" {
		t.Fatalf("ReadArticleBody legacy body=%q err=%v", body, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	body, err = svc.ReadArticleBody(t.Context(), article)
	if err != nil || body != "legacy body" {
		t.Fatalf("ReadArticleBody backfilled body=%q err=%v", body, err)
	}
}

func TestServiceDeleteArticleCascadesContent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewStore(db)
	svc := NewService(store, NewFileManager(t.TempDir()), t.TempDir())
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

func articleAbsPath(home string, article *Article) string {
	if filepath.IsAbs(article.FilePath) {
		return article.FilePath
	}
	return filepath.Join(home, article.FilePath)
}

func TestServiceAuthorizedIdentityAndMissingMapping(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), NewFileManager(t.TempDir()), t.TempDir())
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
