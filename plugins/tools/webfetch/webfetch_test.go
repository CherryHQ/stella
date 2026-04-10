package webfetch

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	readability "codeberg.org/readeck/go-readability/v2"
)

func newTestHTTPServer(t *testing.T, handler http.Handler) (srv *httptest.Server) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil {
			t.Skipf("local test server unavailable: %v", r)
		}
	}()

	if port := os.Getenv("PORT"); port != "" {
		ln, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Skipf("listen on PORT=%q: %v", port, err)
		}
		srv = httptest.NewUnstartedServer(handler)
		srv.Listener = ln
		srv.Start()
		return srv
	}

	return httptest.NewServer(handler)
}

func TestWebFetchTool_Definition(t *testing.T) {
	tool := New()
	def := tool.Definition()
	if def.Name != "webfetch" {
		t.Errorf("expected name 'webfetch', got %q", def.Name)
	}
}

func makeArticleServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>Test Article</title></head><body><article><p>Hello world. This is test content for rendering. It has enough words to be parsed successfully by the readability library.</p><p>Second paragraph for completeness and more content here.</p></article></body></html>`)
	}))
}

func TestWebFetchTool_FormatText(t *testing.T) {
	srv := makeArticleServer(t)
	defer srv.Close()

	tool := New()
	result, err := tool.Execute(context.Background(), map[string]any{
		"url":    srv.URL,
		"format": formatText,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty text result")
	}
}

func TestWebFetchTool_FormatHTML(t *testing.T) {
	srv := makeArticleServer(t)
	defer srv.Close()

	tool := New()
	result, err := tool.Execute(context.Background(), map[string]any{
		"url":    srv.URL,
		"format": formatHTML,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Error("expected non-empty HTML result")
	}
}

func TestWebFetchTool_FormatJSON(t *testing.T) {
	srv := makeArticleServer(t)
	defer srv.Close()

	tool := New()
	result, err := tool.Execute(context.Background(), map[string]any{
		"url":    srv.URL,
		"format": formatJSON,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, `"url"`) {
		t.Errorf("expected JSON with url field, got: %q", result)
	}
}

func TestWebFetchToolNoContent(t *testing.T) {
	// Serve minimal HTML that readability cannot extract content from (nil Node).
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
