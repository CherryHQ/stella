package webfetch

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func useClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := publicClient
	publicClient = client
	t.Cleanup(func() { publicClient = previous })
}

func TestExtractReturnsArticleMetadataAndMarkdown(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>Extracted title</title><meta name="author" content="Author"><meta name="description" content="Blurb"></head><body><article><h1>Extracted title</h1>` + strings.Repeat("<p>Body paragraph with enough words to count as readable content.</p>", 8) + `</article></body></html>`))
	}))
	defer srv.Close()
	useClient(t, http.DefaultClient)

	article, err := Extract(t.Context(), srv.URL+"/post")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if article.Title != "Extracted title" || article.Author != "Author" || article.Description != "Blurb" {
		t.Fatalf("article metadata = %+v", article)
	}
	if !strings.Contains(article.Markdown, "Body paragraph") || article.URL != srv.URL+"/post" {
		t.Fatalf("article = %+v, want markdown body and URL", article)
	}
}

func TestExtractKeepsPlainTextVerbatim(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Already markdown\n\nkeep me"))
	}))
	defer srv.Close()
	useClient(t, http.DefaultClient)

	article, err := Extract(t.Context(), srv.URL+"/doc.md")
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if article.Markdown != "# Already markdown\n\nkeep me" || article.Title != "" {
		t.Fatalf("article = %+v, want verbatim body and no metadata", article)
	}
}

func TestExtractRejectsNonHTTPURL(t *testing.T) {
	if _, err := Extract(t.Context(), "file:///etc/passwd"); err == nil || !strings.Contains(err.Error(), "unsupported scheme") {
		t.Fatalf("Extract() error = %v, want unsupported scheme", err)
	}
}

func TestExtractFallsBackToJinaReader(t *testing.T) {
	var readerRequest *http.Request
	useClient(t, &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
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
	})})

	article, err := Extract(t.Context(), "https://1.1.1.1/article")
	if err != nil {
		t.Fatal(err)
	}
	if readerRequest == nil || readerRequest.URL.String() != jinaReaderBaseURL+"https://1.1.1.1/article" {
		t.Fatalf("Jina Reader request = %v", readerRequest)
	}
	if readerRequest.Header.Get("Accept") != "text/markdown" || readerRequest.Header.Get("X-No-Cache") != "true" {
		t.Fatalf("Jina Reader headers = %#v", readerRequest.Header)
	}
	if !strings.Contains(article.Markdown, "Reader result") {
		t.Fatalf("article = %+v, want Jina Reader content", article)
	}
}

func TestExtractRejectsUnsupportedContentType(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4"))
	}))
	defer srv.Close()
	useClient(t, http.DefaultClient)

	if _, err := Extract(t.Context(), srv.URL+"/doc.pdf"); err == nil || !strings.Contains(err.Error(), "unsupported content type") {
		t.Fatalf("Extract() error = %v, want unsupported content type", err)
	}
}

func TestExtractRejectsLargeBodyBeforeParsing(t *testing.T) {
	srv := newTestHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.Copy(w, io.LimitReader(strings.NewReader(strings.Repeat("<p>x</p>", maxBodySize)), maxBodySize+1024))
	}))
	defer srv.Close()
	useClient(t, http.DefaultClient)

	if _, err := Extract(t.Context(), srv.URL+"/huge"); err == nil || !strings.Contains(err.Error(), "exceeds 10 MB limit") {
		t.Fatalf("Extract() error = %v, want body limit", err)
	}
}
