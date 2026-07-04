package recally

import (
	"errors"
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
	body, err := svc.ReadArticleBody(first.Article)
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
