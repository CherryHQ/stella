package recally

import (
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// A URL with neither content nor content_path is a per-item error: the server
// no longer fetches pages, the model reads them with the web skill and hands
// over a file.
func TestRecallyToolSaveRejectsBareURLWithoutBody(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	tool := NewTool(svc, actionSpec("article_save"))

	out, err := tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/bare", "source_type": "web"},
	}})
	if err != nil {
		t.Fatalf("Execute() error = %v, want a per-item error", err)
	}
	for _, want := range []string{`"status":"error"`, "web skill", "content_path"} {
		if !strings.Contains(out, want) {
			t.Fatalf("save result %q must tell the model to %q", out, want)
		}
	}
	articles, err := mustAccess(t, svc, testUserID).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 0 {
		t.Fatalf("articles=%d err=%v, want nothing stored", len(articles), err)
	}
}

// A supplied body under the char floor reads as a stub or paywall page and is
// rejected with the same thin-extraction message, wherever the body came from.
func TestRecallyToolSaveRejectsThinBody(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	tool := NewTool(svc, actionSpec("article_save"))

	out, err := tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/thin", "content": "too short"},
		map[string]any{"url": "https://example.com/full", "content": strings.Repeat("article body ", 20)},
	}})
	if err != nil {
		t.Fatalf("Execute() error = %v, want per-item errors", err)
	}
	for _, want := range []string{"thin extraction", `"status":"created"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("save result %q must contain %q", out, want)
		}
	}
	articles, err := mustAccess(t, svc, testUserID).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 1 {
		t.Fatalf("articles=%d err=%v, want only the full article stored", len(articles), err)
	}
}

// The content_path flow is the capture path: the model reads the page with the
// web skill, writes the markdown to a sandbox file, and hands the file over.
func TestRecallyToolSaveStoresContentPathBody(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	body := "# Captured\n\n" + strings.Repeat("article body ", 30)
	session := recallyFileSession{Session: pkgsandbox.NopSession(), files: recallyFileAccess{files: map[string][]byte{
		"/tmp/session/captured.md": []byte(body),
	}}}
	tool := NewRuntimeTool(svc, session, actionSpec("article_save"))

	out, err := tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/captured", "title": "Captured", "content_path": "$TMPDIR/captured.md"},
	}})
	if err != nil || !strings.Contains(out, `"status":"created"`) {
		t.Fatalf("save output=%q err=%v", out, err)
	}
	articles, err := mustAccess(t, svc, testUserID).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if err != nil || len(articles) != 1 {
		t.Fatalf("articles=%d err=%v", len(articles), err)
	}
	if got, err := mustAccess(t, svc, testUserID).ReadArticleBody(t.Context(), &articles[0]); err != nil || got != body {
		t.Fatalf("stored body=%q err=%v", got, err)
	}

	// A saved URL keeps the metadata-only update: no body needed, nothing rejected.
	out, err = tool.Execute(recallyFileToolContext(), map[string]any{"articles": []any{
		map[string]any{"url": "https://example.com/captured", "summary": "Model summary"},
	}})
	if err != nil || !strings.Contains(out, `"status":"updated"`) {
		t.Fatalf("second save output=%q err=%v, want metadata-only update", out, err)
	}
}
