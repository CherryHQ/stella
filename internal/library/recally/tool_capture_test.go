package recally

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/webfetch"
)

func TestRecallyToolSaveCapturesNewArticleServerSide(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	body := "# Captured\n\n" + strings.Repeat("article body ", 30)
	calls := 0
	svc.extract = func(_ context.Context, rawURL string) (webfetch.Article, error) {
		calls++
		return webfetch.Article{URL: rawURL, Title: "Captured title", Author: "Author", Description: "Blurb", Published: "2024-01-02T03:04:05Z", Markdown: body}, nil
	}
	tool := NewTool(svc, actionSpec("article_save"))

	out, err := tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/captured", "author": "Caller author"},
	}})
	if err != nil || !strings.Contains(out, `"status":"created"`) {
		t.Fatalf("save output=%q err=%v", out, err)
	}
	if !strings.Contains(out, `"content_preview":"# Captured article body`) || !strings.Contains(out, "[…]") {
		t.Fatalf("save result %q must preview the captured body", out)
	}
	acc := mustAccess(t, svc, testUserID)
	articles, err := acc.ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 1 {
		t.Fatalf("articles=%d err=%v", len(articles), err)
	}
	// Page metadata fills what the caller left empty and never overrides it.
	if articles[0].Title != "Captured title" || articles[0].Author != "Caller author" || articles[0].Summary != "Blurb" || articles[0].PublishedAt == nil {
		t.Fatalf("article = %+v, want page metadata under caller-supplied fields", articles[0])
	}
	if got, err := acc.ReadArticleBody(t.Context(), &articles[0]); err != nil || got != body {
		t.Fatalf("stored body=%q err=%v", got, err)
	}

	// A saved URL keeps the metadata-only update: no second fetch.
	out, err = tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/captured", "summary": "Model summary"},
	}})
	if err != nil || !strings.Contains(out, `"status":"updated"`) || calls != 1 {
		t.Fatalf("second save output=%q err=%v calls=%d, want metadata-only update", out, err, calls)
	}
}

func TestRecallyToolSaveReportsCaptureFailuresPerItem(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	svc.extract = func(_ context.Context, rawURL string) (webfetch.Article, error) {
		if strings.HasSuffix(rawURL, "/thin") {
			return webfetch.Article{Markdown: "too short"}, nil
		}
		return webfetch.Article{}, errors.New("web_fetch: HTTP 404")
	}
	tool := NewTool(svc, actionSpec("article_save"))

	out, err := tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/thin"},
		map[string]any{"url": "https://example.com/missing"},
		map[string]any{"url": "https://example.com/inline", "content": strings.Repeat("inline body ", 20)},
	}})
	if err != nil {
		t.Fatalf("Execute() error = %v, want per-item errors", err)
	}
	for _, want := range []string{"thin extraction", "fetch: web_fetch: HTTP 404", `"status":"created"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("save result %q must contain %q", out, want)
		}
	}
	articles, err := mustAccess(t, svc, testUserID).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 1 {
		t.Fatalf("articles=%d err=%v, want only the inline article stored", len(articles), err)
	}
}
