package share

import (
	"strings"
	"testing"
)

func TestRenderMarkdownPage(t *testing.T) {
	md := []byte("# Hello\n\nThis is **bold** and a [link](https://example.com).\n\n```go\nfmt.Println(\"hi\")\n```\n")
	out, err := RenderMarkdownPage(RenderMarkdownOpts{
		Title:     "Test Article",
		Author:    "Alice",
		ExpiresAt: "2026-06-01",
	}, md)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if !strings.Contains(html, "<title>Test Article</title>") {
		t.Error("missing title")
	}
	if !strings.Contains(html, "Alice") {
		t.Error("missing author")
	}
	if !strings.Contains(html, "2026-06-01") {
		t.Error("missing expiry")
	}
	if !strings.Contains(html, "<strong>bold</strong>") {
		t.Error("missing bold rendering")
	}
	if !strings.Contains(html, "<a href=\"https://example.com\">link</a>") {
		t.Error("missing link rendering")
	}
	if !strings.Contains(html, "<code") {
		t.Error("missing code block rendering")
	}
}

func TestRenderMarkdownPageNoMeta(t *testing.T) {
	md := []byte("Just a paragraph.")
	out, err := RenderMarkdownPage(RenderMarkdownOpts{Title: "Untitled"}, md)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if strings.Contains(html, `class="meta"`) {
		t.Error("meta section should be hidden when no author or expiry")
	}
}

func TestRenderMarkdownPageGFMTable(t *testing.T) {
	md := []byte("| A | B |\n|---|---|\n| 1 | 2 |\n")
	out, err := RenderMarkdownPage(RenderMarkdownOpts{Title: "Table Test"}, md)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "<table>") {
		t.Error("GFM table not rendered")
	}
}

func TestRenderMarkdownPageWithArticleMeta(t *testing.T) {
	md := []byte("Article body content.")
	out, err := RenderMarkdownPage(RenderMarkdownOpts{
		Title:     "Deep Learning Guide",
		Author:    "Bob",
		SourceURL: "https://example.com/article",
		Summary:   "A comprehensive guide to **deep learning** techniques.",
		Tags:      []string{"AI", "ML", "tutorial"},
	}, md)
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	if !strings.Contains(html, "Bob") {
		t.Error("missing author")
	}
	if !strings.Contains(html, "https://example.com/article") {
		t.Error("missing source URL")
	}
	if !strings.Contains(html, "AI Summary") {
		t.Error("missing summary section")
	}
	if !strings.Contains(html, "<strong>deep learning</strong>") {
		t.Error("summary markdown not rendered")
	}
	if !strings.Contains(html, "AI") || !strings.Contains(html, "ML") || !strings.Contains(html, "tutorial") {
		t.Error("missing tags")
	}
}
