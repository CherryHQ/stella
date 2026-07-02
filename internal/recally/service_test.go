package recally

import (
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/toolctx"
)

func TestServiceSaveOwnedWritesFilesAndDedups(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	home := t.TempDir()
	svc := NewService(NewStore(db), NewFileManager(home), home)
	ident := toolctx.Identity{UserID: testUserID, AgentID: "agent-a", AgentScoped: true}

	first, err := svc.SaveOwned(t.Context(), ident, SaveRequest{URL: "https://example.com/a", Title: "A", Content: "first"})
	if err != nil {
		t.Fatalf("SaveOwned first: %v", err)
	}
	if !first.Created || first.Article.FilePath == "" {
		t.Fatalf("first=%+v, want created with file", first)
	}
	body, err := svc.ReadArticleBody(first.Article)
	if err != nil || body != "first" {
		t.Fatalf("ReadArticleBody body=%q err=%v", body, err)
	}
	second, err := svc.SaveOwned(t.Context(), ident, SaveRequest{URL: "https://example.com/a?utm_source=x", CanonicalURL: first.Article.CanonicalURL, Title: "A2", Content: "second"})
	if err != nil {
		t.Fatalf("SaveOwned second: %v", err)
	}
	if second.Created || second.Article.ID != first.Article.ID {
		t.Fatalf("second=%+v, want update of same article %s", second, first.Article.ID)
	}
	articles, err := svc.ListArticlesOwned(t.Context(), ident, ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 1 {
		t.Fatalf("ListArticlesOwned len=%d err=%v", len(articles), err)
	}
}

func TestServiceOwnedIdentityAndMissingMapping(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), NewFileManager(t.TempDir()), t.TempDir())
	if _, err := svc.ListArticlesOwned(t.Context(), toolctx.Identity{}, ArticleFilter{}); !errors.Is(err, toolctx.ErrUnauthenticated) {
		t.Fatalf("ListArticlesOwned unauth err=%v, want ErrUnauthenticated", err)
	}
	if _, err := svc.GetArticleOwned(t.Context(), toolctx.Identity{UserID: testUserID}, "missing"); !errors.Is(err, toolctx.ErrNotFound) {
		t.Fatalf("GetArticleOwned missing err=%v, want ErrNotFound", err)
	}
	_, err := svc.SaveOwned(t.Context(), toolctx.Identity{UserID: testUserID}, SaveRequest{URL: "https://example.com/new"})
	if err == nil || !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("SaveOwned missing content err=%v", err)
	}
}
