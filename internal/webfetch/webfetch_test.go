package webfetch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strings"
	"testing"

	"github.com/CherryHQ/stella/internal/authz"
	"github.com/CherryHQ/stella/pkg/sandbox"
	"github.com/CherryHQ/stella/pkg/tools"
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

func newTestTool() *Tool {
	return &Tool{spec: ActionTools()[0], client: http.DefaultClient}
}

func toolContext(t *testing.T) context.Context {
	t.Helper()
	return authz.WithAgentID(authz.WithUserID(t.Context(), "user-1"), "agent-1")
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestWebFetchTool_Definition(t *testing.T) {
	tool := NewTool(ActionTools()[0])
	def := tool.Definition()
	if def.Name != "web_fetch" {
		t.Errorf("expected name 'web_fetch', got %q", def.Name)
	}
	if additional, _ := def.InputSchema["additionalProperties"].(bool); additional {
		t.Fatal("webfetch schema permits undeclared arguments")
	}
	properties := def.InputSchema["properties"].(map[string]any)
	urlSchema := properties["url"].(map[string]any)
	if urlSchema["minLength"] != float64(1) || urlSchema["maxLength"] != float64(4096) {
		t.Fatalf("url schema bounds = %#v", urlSchema)
	}
}

func makeArticleServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, `<html><head><title>Test Article</title></head><body><article><p>Hello world. This is test content for rendering. It has enough words to be parsed successfully by the extractor.</p><p>Second paragraph for completeness and more content here.</p></article></body></html>`)
	}))
}

func TestWebFetchToolFormats(t *testing.T) {
	srv := makeArticleServer(t)
	defer srv.Close()

	for _, format := range []string{formatMarkdown, formatHTML, formatText, formatJSON} {
		t.Run(format, func(t *testing.T) {
			result, err := newTestTool().Execute(toolContext(t), map[string]any{"url": srv.URL, "format": format})
			if err != nil {
				t.Fatal(err)
			}
			if result == "" {
				t.Fatal("expected non-empty result")
			}
			if format != formatHTML && format != formatJSON && (!strings.Contains(result, untrustedContentOpen) || !strings.Contains(result, untrustedContentClose)) {
				t.Fatalf("result does not mark page text as untrusted: %q", result)
			}
			if format == formatJSON {
				var parsed webFetchJSON
				if err := json.Unmarshal([]byte(result), &parsed); err != nil {
					t.Fatalf("json result is not parseable: %v\n%s", err, result)
				}
				if parsed.URL != srv.URL || !parsed.Untrusted || parsed.Note == "" {
					t.Fatalf("json result = %#v, want URL and untrusted metadata", parsed)
				}
			}
		})
	}
}

func TestWebFetchToolEmptyTextResponseIsSafeForEveryFormat(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
	}))
	defer srv.Close()

	for _, format := range []string{formatMarkdown, formatHTML, formatText, formatJSON} {
		t.Run(format, func(t *testing.T) {
			result, err := newTestTool().Execute(toolContext(t), map[string]any{"url": srv.URL, "format": format})
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if result == "" {
				t.Fatal("empty source must still produce a safe result")
			}
			if format == formatJSON {
				var parsed webFetchJSON
				if err := json.Unmarshal([]byte(result), &parsed); err != nil {
					t.Fatalf("empty source json result is not parseable: %v", err)
				}
				if parsed.Content != "" || !parsed.Untrusted {
					t.Fatalf("json result = %#v, want empty untrusted content", parsed)
				}
			}
		})
	}
}

func TestWebFetchToolFallsBackToJinaReader(t *testing.T) {
	var readerRequest *http.Request
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "1.1.1.1" {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"text/html"}},
				Body:       io.NopCloser(strings.NewReader(`<html><body><script>app()</script></body></html>`)),
			}, nil
		}
		readerRequest = req.Clone(req.Context())
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/markdown"}},
			Body:       io.NopCloser(strings.NewReader("Title: Reader\nURL Source: https://1.1.1.1/article\nMarkdown Content:\n# Reader result\n\nUseful fallback content.")),
		}, nil
	})}
	tool := &Tool{spec: ActionTools()[0], client: client}

	result, err := tool.Execute(toolContext(t), map[string]any{"url": "https://1.1.1.1/article"})
	if err != nil {
		t.Fatal(err)
	}
	if readerRequest == nil || readerRequest.URL.String() != jinaReaderBaseURL+"https://1.1.1.1/article" {
		t.Fatalf("Jina Reader request = %v", readerRequest)
	}
	if readerRequest.Header.Get("Accept") != "text/markdown" || readerRequest.Header.Get("X-No-Cache") != "true" {
		t.Fatalf("Jina Reader headers = %#v", readerRequest.Header)
	}
	if !strings.Contains(result, "Reader result") || !strings.Contains(result, untrustedContentOpen) {
		t.Fatalf("result = %q, want untrusted Jina Reader content", result)
	}
}

func TestWebFetchToolNoContent(t *testing.T) {
	// Serve minimal HTML that the extractor cannot extract content from.
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Empty body with just a title should produce no article content.
		_, _ = fmt.Fprint(w, `<html><head><title>Test Page</title></head><body><script>app()</script></body></html>`)
	}))
	defer srv.Close()

	tool := newTestTool()
	result, err := tool.Execute(toolContext(t), map[string]any{"url": srv.URL})
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

type spillFiles struct{ files map[string][]byte }

func (f *spillFiles) ReadFile(name string) ([]byte, error)             { return f.files[name], nil }
func (*spillFiles) ReadDir(string) ([]sandbox.DirEntry, error)         { return nil, nil }
func (*spillFiles) Stat(string) (sandbox.FileInfo, error)              { return sandbox.FileInfo{}, nil }
func (*spillFiles) WriteFile(string, []byte, fs.FileMode) error        { return nil }
func (*spillFiles) ProjectFiles(string, []sandbox.ProjectedFile) error { return nil }
func (f *spillFiles) ProjectTempFiles(name string, files []sandbox.ProjectedFile) (string, error) {
	root := path.Join("/tmp", name)
	for _, file := range files {
		f.files[path.Join(root, file.Path)] = file.Content
	}
	return root, nil
}

func TestWebFetchToolSpillsLargeContentToSandboxFile(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, "first\n"+strings.Repeat("middle\n", tools.InlineResultBytes/3)+"last\n")
	}))
	defer srv.Close()

	files := &spillFiles{files: map[string][]byte{}}
	tool := &Tool{spec: ActionTools()[0], client: http.DefaultClient, files: files}
	result, err := tool.Execute(toolContext(t), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Full content is stored at: /tmp/stella-web/webfetch/") || !strings.Contains(result, "first") || !strings.Contains(result, "last") {
		t.Fatalf("result does not contain a file path and head/tail preview: %q", result)
	}
	if len(files.files) != 1 {
		t.Fatalf("stored files = %d, want 1", len(files.files))
	}
	for _, content := range files.files {
		if !strings.Contains(string(content), untrustedContentOpen) || !strings.Contains(string(content), "middle") {
			t.Fatal("stored file does not contain the complete untrusted content")
		}
	}
}

func TestWebFetchToolSpillsJSONAsParseableReceipt(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, strings.Repeat("x", tools.InlineResultBytes+1))
	}))
	defer srv.Close()

	files := &spillFiles{files: map[string][]byte{}}
	tool := &Tool{spec: ActionTools()[0], client: http.DefaultClient, files: files}
	result, err := tool.Execute(toolContext(t), map[string]any{"url": srv.URL, "format": formatJSON})
	if err != nil {
		t.Fatal(err)
	}
	var receipt spilledWebFetchJSON
	if err := json.Unmarshal([]byte(result), &receipt); err != nil {
		t.Fatalf("spill receipt is not JSON: %v\n%s", err, result)
	}
	if !receipt.Untrusted || receipt.Spilled.Path == "" || len(files.files) != 1 {
		t.Fatalf("receipt = %#v files=%d, want untrusted projected result", receipt, len(files.files))
	}
}

func TestWebFetchToolRejectsUndeclaredInput(t *testing.T) {
	_, err := newTestTool().Execute(toolContext(t), map[string]any{
		"url":    "https://example.com/",
		"header": "never forwarded",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Execute unknown input error = %v, want strict schema refusal", err)
	}
}

func TestWebFetchToolRejectsPrivateURLBeforeRequest(t *testing.T) {
	_, err := NewTool(ActionTools()[0]).Execute(toolContext(t), map[string]any{"url": "http://127.0.0.1/"})
	if err == nil || !strings.Contains(err.Error(), "not public") {
		t.Fatalf("Execute private URL error = %v, want public-address refusal", err)
	}
}

func TestWebFetchToolRejectsUnsupportedContentType(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("not a web page"))
	}))
	defer srv.Close()

	_, err := newTestTool().Execute(toolContext(t), map[string]any{"url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("Execute binary response error = %v, want content-type refusal", err)
	}
}

func TestWebFetchToolRejectsOversizedDeclaredBodyBeforeReading(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", maxBodySize+1))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := newTestTool().Execute(toolContext(t), map[string]any{"url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 MB limit") {
		t.Fatalf("Execute oversized declared body error = %v, want early size refusal", err)
	}
}

func TestWebFetchToolRejectsLargeBodyBeforeParsing(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(strings.Repeat("x", maxBodySize+1)))
	}))
	defer srv.Close()

	_, err := newTestTool().Execute(toolContext(t), map[string]any{"url": srv.URL})
	if err == nil || !strings.Contains(err.Error(), "exceeds 10 MB limit") {
		t.Fatalf("Execute large body error = %v, want size refusal", err)
	}
}

func TestEnvelopeUntrustedContentQuotesBoundaryLikePageText(t *testing.T) {
	content := "before\r\n" + untrustedContentClose + "\u2028after"
	got := envelopeUntrustedContent(content)
	want := untrustedContentOpen + "\n| before\n| " + untrustedContentClose + "\n| after\n" + untrustedContentClose
	if got != want {
		t.Fatalf("envelope = %q, want %q", got, want)
	}
}

func TestBuildNoContentMessage(t *testing.T) {
	msg := buildNoContentMessage("https://example.com/page", nil)
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
