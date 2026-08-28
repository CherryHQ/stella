package recally

import (
	"strings"
	"testing"

	pkgsandbox "github.com/CherryHQ/stella/pkg/sandbox"
)

// The split schema and DecodeInputStrict reject unknown arguments at the tool
// boundary, so a misspelled field is a visible error instead of a silently
// dropped one. That is the whole point of sealing the schema.
func TestRecallySaveArticleRejectsAnUnknownField(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	session := recallyFileSession{Session: pkgsandbox.NopSession(), files: recallyFileAccess{files: map[string][]byte{}}}
	tool := NewRuntimeTool(svc, session, actionSpec("save_article"))

	_, err := tool.Execute(recallyFileToolContext(), map[string]any{
		"articles": []any{map[string]any{"url": "https://example.com/a", "title": "A"}},
		"titel":    "typo",
	})
	if err == nil || !strings.Contains(err.Error(), `unknown field "titel"`) {
		t.Fatalf("err=%v, want an unknown-field rejection", err)
	}
	if !strings.Contains(err.Error(), "this tool accepts:") {
		t.Fatalf("err=%v must list the accepted fields", err)
	}
	articles, listErr := mustAccess(t, svc, testUserID).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if listErr != nil || len(articles) != 0 {
		t.Fatalf("rejected call still wrote: articles=%d err=%v", len(articles), listErr)
	}
}

// A required field *inside* a batch item is caught by a different layer. The
// generated item schema does declare `required: ["url"]`, so a provider-issued
// call is refused structurally before it reaches us. A direct Tool.Execute call
// has no provider validating anything, and DecodeInputStrict does not walk into
// nested required lists — so the service rejects it as defense in depth. It
// must still be an error the model can read, and it must not write anything.
func TestRecallySaveArticleItemWithoutURLIsRejectedWithoutWriting(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	svc := NewService(NewStore(db), t.TempDir())
	session := recallyFileSession{Session: pkgsandbox.NopSession(), files: recallyFileAccess{files: map[string][]byte{}}}
	tool := NewRuntimeTool(svc, session, actionSpec("save_article"))

	out, err := tool.Execute(recallyFileToolContext(), map[string]any{
		"articles": []any{map[string]any{"title": "x"}},
	})
	if err != nil {
		t.Fatalf("err=%v; the batch reports per-item failures in its result", err)
	}
	if !strings.Contains(out, `"status":"error"`) || !strings.Contains(out, "url is required") {
		t.Fatalf("out=%q, want a per-item url-is-required error", out)
	}
	articles, listErr := mustAccess(t, svc, testUserID).ListArticles(t.Context(), ArticleFilter{Limit: 10})
	if listErr != nil || len(articles) != 0 {
		t.Fatalf("failed item still wrote: articles=%d err=%v", len(articles), listErr)
	}
}
