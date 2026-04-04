package webfetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	readability "codeberg.org/readeck/go-readability/v2"
)

func TestWebFetchToolNoContent(t *testing.T) {
	// Serve minimal HTML that readability cannot extract content from (nil Node).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Empty body with just a title — readability will parse but produce nil Node.
		_, _ = fmt.Fprint(w, `<html><head><title>Test Page</title></head><body><script>app()</script></body></html>`)
	}))
	defer srv.Close()

	tool := New()
	result, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("expected no error for nil-Node page, got: %v", err)
	}
	if !strings.Contains(result, "No readable content") {
		t.Errorf("expected fallback message, got: %q", result)
	}
	if !strings.Contains(result, "Test Page") {
		t.Errorf("expected title in fallback, got: %q", result)
	}
}

func TestWebFetchToolSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>Article</title></head><body><article><p>Hello world. This is a test article with enough content for readability to extract.</p><p>Second paragraph with more details about the topic at hand.</p></article></body></html>`)
	}))
	defer srv.Close()

	tool := New()
	result, err := tool.Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty result")
	}
}

func TestBuildNoContentMessage(t *testing.T) {
	msg := buildNoContentMessage("https://example.com/page", readability.Article{})
	if !strings.Contains(msg, "No readable content") {
		t.Error("missing header")
	}
	if !strings.Contains(msg, "https://example.com/page") {
		t.Error("missing URL")
	}
	if !strings.Contains(msg, "JavaScript") {
		t.Error("missing guidance hint")
	}
}
